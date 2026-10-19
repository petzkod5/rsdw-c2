package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"
)

const (
	sampleServerName = "Example server"
	sampleImage      = "ghcr.io/petzkod5/rsdragonwilds-server:v1.2.3"
)

// sampleMessageEvent builds the representative event used by previews and kind-specific tests. It carries the
// evidence a real event of the kind would, so every variable the kind supports has a value.
func sampleMessageEvent(kind EventKind, server *Server, actor string, now time.Time) Event {
	event := Event{ID: randomID(), Timestamp: now.UTC(), Kind: kind, Message: "Sample " + string(kind), Source: "C2 operator", Accuracy: "observed", Actor: actor}
	maxPlayers := 10
	if kind != IntegrationTest || server != nil {
		event.ServerName, event.Image = sampleServerName, sampleImage
	}
	if server != nil {
		event.ServerID, event.ServerName = server.ID, worldLabel(*server)
		if server.CurrentImage != "" {
			event.Image = server.CurrentImage
		} else if server.DesiredImage != "" {
			event.Image = server.DesiredImage
		}
		if server.MaxPlayers > 0 {
			maxPlayers = server.MaxPlayers
		}
	}
	switch kind {
	case PlayerJoined:
		event.Evidence.PlayerCount = &PlayerCountEvidence{Available: true, Players: min(3, maxPlayers), MaxPlayers: maxPlayers}
		event.Evidence.JoinedPlayers = []ConnectedPlayer{{Name: "Sam", CharacterName: "Brynn"}}
	case ServerRecovered:
		event.Evidence.PlayerCount = &PlayerCountEvidence{Available: true, Players: min(3, maxPlayers), MaxPlayers: maxPlayers}
	case RestartWarning:
		event.Evidence.RestartWarning = &RestartWarningEvidence{Trigger: "sample", RestartAt: now.UTC().Add(5 * time.Minute), Minutes: 5}
	}
	return event
}

// decodeOptionalBody reads a JSON object body that may be empty.
func decodeOptionalBody(w http.ResponseWriter, r *http.Request, into any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, integrationBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil && err != io.EOF {
		return err
	}
	return nil
}

func writeFieldError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "fields": integrationErrorFields(err)})
}

type messageTarget struct {
	integration DiscordIntegration
	server      *Server
	kind        EventKind
}

// resolveMessageTarget validates the kind and optional server of a test or preview request against the saved
// integration.
func resolveMessageTarget(snapshot State, id string, kind EventKind, serverID string) (messageTarget, int, error) {
	integration, ok := snapshot.Integrations[id]
	if !ok {
		return messageTarget{}, http.StatusNotFound, errors.New("integration not found")
	}
	target := messageTarget{integration: integration, kind: kind}
	if _, ok := discordSpec(kind); !ok {
		return target, http.StatusBadRequest, messageFieldError("kind", "unknown event kind %q", string(kind))
	}
	if serverID != "" {
		server, exists := snapshot.Servers[serverID]
		if !exists || !slices.Contains(integration.ServerIDs, serverID) {
			return target, http.StatusBadRequest, messageFieldError("serverId", "server %q is not associated with this integration", serverID)
		}
		target.server = &server
	}
	return target, http.StatusOK, nil
}

func (a *App) handleIntegrationTest(w http.ResponseWriter, r *http.Request, id string) {
	var request struct {
		Kind     EventKind `json:"kind"`
		ServerID string    `json:"serverId"`
	}
	if err := decodeOptionalBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid test JSON")
		return
	}
	if request.Kind == "" {
		request.Kind = IntegrationTest
	}
	var delivery Delivery
	status := http.StatusInternalServerError
	err := a.store.Update(func(state *State) error {
		target, code, err := resolveMessageTarget(*state, id, request.Kind, request.ServerID)
		if err != nil {
			status = code
			return err
		}
		if normalizedProvider(target.integration.Provider) != providerDiscord && request.Kind != IntegrationTest {
			status = http.StatusBadRequest
			return messageFieldError("kind", "webhook integrations only support the integration test message")
		}
		event := sampleMessageEvent(request.Kind, target.server, requestPrincipal(r).Subject, time.Now().UTC())
		if request.Kind == IntegrationTest {
			event.Message = "Integration test"
		}
		delivery = queueDelivery(state, target.integration, event, true)
		return nil
	})
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, delivery)
	case status == http.StatusInternalServerError:
		writeError(w, status, "could not persist test delivery")
	case status == http.StatusBadRequest:
		writeFieldError(w, err)
	default:
		writeError(w, status, err.Error())
	}
}

type previewRequest struct {
	Kind           EventKind                                `json:"kind"`
	ServerID       string                                   `json:"serverId"`
	Messages       map[EventKind]MessageTemplate            `json:"messages"`
	ServerMessages map[string]map[EventKind]MessageTemplate `json:"serverMessages"`
}

type previewResponse struct {
	Kind        EventKind     `json:"kind"`
	ServerID    string        `json:"serverId,omitempty"`
	Source      messageSource `json:"source"`
	Enabled     bool          `json:"enabled"`
	Embed       discordEmbed  `json:"embed"`
	SampleEvent Event         `json:"sampleEvent"`
}

// handleMessagePreview renders a sample event with the saved templates, or with the unsaved ones in the request.
// A present messages or serverMessages key replaces the saved map for this preview only. Nothing is persisted and
// no delivery is queued.
func (a *App) handleMessagePreview(w http.ResponseWriter, r *http.Request, id string) {
	var request previewRequest
	if err := decodeOptionalBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid preview JSON")
		return
	}
	target, status, err := resolveMessageTarget(a.store.Snapshot(), id, request.Kind, request.ServerID)
	if err != nil {
		if status == http.StatusBadRequest {
			writeFieldError(w, err)
		} else {
			writeError(w, status, err.Error())
		}
		return
	}
	candidate := target.integration.cloneMessages()
	if request.Messages != nil {
		candidate.Messages = request.Messages
	}
	if request.ServerMessages != nil {
		candidate.ServerMessages = request.ServerMessages
	}
	if err := validateIntegrationMessages(candidate); err != nil {
		writeFieldError(w, err)
		return
	}
	candidate.normalizeMessages()
	effective := candidate.resolveMessage(request.ServerID, request.Kind)
	event := sampleMessageEvent(request.Kind, target.server, requestPrincipal(r).Subject, time.Now().UTC())
	var template *MessageTemplate
	if effective.Source != messageSourceDefault {
		template = &effective.MessageTemplate
	}
	embed, err := renderDiscordEmbed(event, template)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, previewResponse{Kind: request.Kind, ServerID: request.ServerID, Source: effective.Source, Enabled: effective.enabled(), Embed: embed, SampleEvent: event})
}
