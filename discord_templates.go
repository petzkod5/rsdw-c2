package main

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Discord embed limits from https://docs.discord.com/developers/resources/message#embed-object-embed-limits.
const (
	discordTitleLimit       = 256
	discordDescriptionLimit = 4096
	discordEmbedTotalLimit  = 6000
	discordFieldNameLimit   = 256
	discordFieldValueLimit  = 1024
	discordColorMax         = 0xFFFFFF
)

// MessageTemplate is one configurable Discord message. Color is a pointer so a request that omits it falls back
// to the built-in color. Enabled is nil (meaning true) unless a message is switched off.
type MessageTemplate struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       *int   `json:"color"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type messageSource string

const (
	messageSourceDefault     messageSource = "default"
	messageSourceIntegration messageSource = "integration"
	messageSourceServer      messageSource = "server"
)

// EffectiveMessage is a template together with the level that supplied it.
type EffectiveMessage struct {
	MessageTemplate
	Source messageSource `json:"source"`
}

func (m MessageTemplate) enabled() bool { return m.Enabled == nil || *m.Enabled }

func (m MessageTemplate) clone() MessageTemplate {
	if m.Color != nil {
		color := *m.Color
		m.Color = &color
	}
	if m.Enabled != nil {
		enabled := *m.Enabled
		m.Enabled = &enabled
	}
	return m
}

func (m MessageTemplate) equal(other MessageTemplate) bool {
	return m.Title == other.Title && m.Description == other.Description && m.enabled() == other.enabled() &&
		m.Color != nil && other.Color != nil && *m.Color == *other.Color
}

type messageVariable struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Example     string `json:"example"`
}

// Text rendered for a variable that has no value for the event. Server keeps the wording the embed fields used
// before templates existed.
const (
	unknownServerText = "Unknown server"
	unavailableText   = "unavailable"
)

var messageVariableCatalogue = []messageVariable{
	{"event", "Label of the event kind", "Restart completed"},
	{"server", "Server name; \"Unknown server\" when the event has none", "Example server"},
	{"players", "Players online when the event recorded a fresh count", "3"},
	{"maxPlayers", "Configured player limit when the event recorded a count", "10"},
	{"player", "Account names of the players who joined, comma separated", "Sam"},
	{"character", "Character names of the players who joined, comma separated", "Brynn"},
	{"joinedPlayers", "Joined players as \"Character (Account)\", comma separated", "Brynn (Sam)"},
	{"time", "Event time in UTC (RFC 3339)", "2026-01-01T12:00:00Z"},
	{"scheduledTime", "Scheduled restart time in UTC (RFC 3339)", "2026-01-01T12:05:00Z"},
	{"warningMinutes", "Minutes between the warning and the restart", "5"},
	{"image", "Server image the event was raised against", "ghcr.io/petzkod5/rsdragonwilds-server:v1.2.3"},
	{"imageTag", "Tag of that image", "v1.2.3"},
	{"operator", "Admin who triggered the event, when C2 recorded one (test messages)", "admin"},
}

func messageVariableByName(name string) (messageVariable, bool) {
	for _, variable := range messageVariableCatalogue {
		if variable.Name == name {
			return variable, true
		}
	}
	return messageVariable{}, false
}

// messageKindVariables lists the variables that can carry a value for the kind. Any catalogue variable is
// accepted in any template and renders as "unavailable" where it has no value.
func messageKindVariables(kind EventKind) []messageVariable {
	names := []string{"event", "time"}
	switch kind {
	case IntegrationTest:
		names = append(names, "operator")
	case RestartWarning:
		names = append(names, "server", "scheduledTime", "warningMinutes", "image", "imageTag")
	case PlayerJoined:
		names = append(names, "server", "players", "maxPlayers", "player", "character", "joinedPlayers", "image", "imageTag")
	case ServerRecovered:
		names = append(names, "server", "players", "maxPlayers", "image", "imageTag")
	default:
		names = append(names, "server", "image", "imageTag")
	}
	var out []messageVariable
	for _, variable := range messageVariableCatalogue {
		if slices.Contains(names, variable.Name) {
			out = append(out, variable)
		}
	}
	return out
}

var placeholderPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

func unknownPlaceholder(text string) (string, bool) {
	for _, match := range placeholderPattern.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(match[1])
		if _, ok := messageVariableByName(name); !ok {
			return name, true
		}
	}
	return "", false
}

func messageVariableValues(event Event) map[string]string {
	kindLabel := string(event.Kind)
	if spec, ok := discordSpec(event.Kind); ok {
		kindLabel = spec.Label
	}
	values := map[string]string{"event": kindLabel, "server": unknownServerText}
	for _, variable := range messageVariableCatalogue {
		if _, set := values[variable.Name]; !set {
			values[variable.Name] = unavailableText
		}
	}
	if event.ServerName != "" {
		values["server"] = event.ServerName
	}
	if !event.Timestamp.IsZero() {
		values["time"] = event.Timestamp.UTC().Format(time.RFC3339)
	}
	if count := event.Evidence.PlayerCount; count != nil {
		values["maxPlayers"] = strconv.Itoa(count.MaxPlayers)
		if count.Available {
			values["players"] = strconv.Itoa(count.Players)
		}
	}
	if joined := event.Evidence.JoinedPlayers; len(joined) > 0 {
		var accounts, characters, both []string
		for _, player := range joined {
			characters = append(characters, player.CharacterName)
			entry := player.CharacterName
			if player.Name != "" {
				accounts = append(accounts, player.Name)
				entry += " (" + player.Name + ")"
			}
			both = append(both, entry)
		}
		values["character"], values["joinedPlayers"] = strings.Join(characters, ", "), strings.Join(both, ", ")
		if len(accounts) > 0 {
			values["player"] = strings.Join(accounts, ", ")
		}
	}
	if warning := event.Evidence.RestartWarning; warning != nil {
		values["scheduledTime"] = warning.RestartAt.UTC().Format(time.RFC3339)
		values["warningMinutes"] = strconv.Itoa(warning.Minutes)
	}
	if event.Image != "" {
		values["image"] = event.Image
		if tag := imageTagOf(event.Image); tag != "" {
			values["imageTag"] = tag
		}
	}
	if event.Actor != "" {
		values["operator"] = event.Actor
	}
	return values
}

// imageTagOf returns the tag of "registry[:port]/repo:tag[@digest]".
func imageTagOf(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	colon := strings.LastIndex(image, ":")
	if colon < 0 || strings.Contains(image[colon:], "/") {
		return ""
	}
	return image[colon+1:]
}

// expandTemplate substitutes {{name}} placeholders in one pass. Substituted values are never re-expanded, and
// text that is not a catalogue placeholder is left alone.
func expandTemplate(text string, values map[string]string) string {
	return placeholderPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := strings.TrimSpace(placeholderPattern.FindStringSubmatch(match)[1])
		if value, ok := values[name]; ok {
			return value
		}
		return match
	})
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit-1]) + "…"
}

func embedTotalLength(embed discordEmbed) int {
	total := utf8.RuneCountInString(embed.Title) + utf8.RuneCountInString(embed.Description) + utf8.RuneCountInString(embed.Footer.Text)
	for _, field := range embed.Fields {
		total += utf8.RuneCountInString(field.Name) + utf8.RuneCountInString(field.Value)
	}
	return total
}

// fitEmbed truncates rendered text so variable values cannot push the embed over Discord's limits.
func fitEmbed(embed discordEmbed) discordEmbed {
	embed.Title = truncateRunes(embed.Title, discordTitleLimit)
	embed.Description = truncateRunes(embed.Description, discordDescriptionLimit)
	for i, field := range embed.Fields {
		embed.Fields[i] = discordEmbedField{Name: truncateRunes(field.Name, discordFieldNameLimit), Value: truncateRunes(field.Value, discordFieldValueLimit)}
	}
	if excess := embedTotalLength(embed) - discordEmbedTotalLimit; excess > 0 {
		embed.Description = truncateRunes(embed.Description, max(1, utf8.RuneCountInString(embed.Description)-excess))
	}
	return embed
}

// integrationFieldError is a validation failure tied to one request field, so the UI can mark that field.
type integrationFieldError struct {
	Field   string
	Message string
}

func (e integrationFieldError) Error() string { return e.Message }

func messageFieldError(field, format string, args ...any) error {
	return integrationFieldError{Field: field, Message: field + ": " + fmt.Sprintf(format, args...)}
}

func validateMessageTemplate(field string, kind EventKind, tmpl MessageTemplate) error {
	for _, part := range []struct {
		name, text string
		limit      int
		singleLine bool
	}{{"title", tmpl.Title, discordTitleLimit, true}, {"description", tmpl.Description, discordDescriptionLimit, false}} {
		path := field + "." + part.name
		if length := utf8.RuneCountInString(part.text); length > part.limit {
			return messageFieldError(path, "%s for %s is %d characters; Discord allows %d", part.name, kind, length, part.limit)
		}
		if strings.ContainsRune(part.text, 0) || (part.singleLine && strings.ContainsAny(part.text, "\r\n")) {
			return messageFieldError(path, "%s for %s must not contain NUL or, for the title, line breaks", part.name, kind)
		}
		if name, unknown := unknownPlaceholder(part.text); unknown {
			return messageFieldError(path, "unknown variable {{%s}} in the %s of %s", name, part.name, kind)
		}
	}
	if strings.TrimSpace(tmpl.Title) == "" && strings.TrimSpace(tmpl.Description) == "" {
		return messageFieldError(field+".title", "%s needs a title or a description", kind)
	}
	if utf8.RuneCountInString(tmpl.Title)+utf8.RuneCountInString(tmpl.Description) > discordEmbedTotalLimit {
		return messageFieldError(field, "title and description for %s exceed Discord's %d character embed limit", kind, discordEmbedTotalLimit)
	}
	if tmpl.Color != nil && (*tmpl.Color < 0 || *tmpl.Color > discordColorMax) {
		return messageFieldError(field+".color", "color for %s must be between 0 and %d", kind, discordColorMax)
	}
	return nil
}

func validateMessageSet(field string, messages map[EventKind]MessageTemplate) error {
	kinds := make([]string, 0, len(messages))
	for kind := range messages {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)
	for _, name := range kinds {
		kind := EventKind(name)
		path := field + "." + name
		if _, ok := discordSpec(kind); !ok {
			return messageFieldError(path, "unknown event kind %q", name)
		}
		if err := validateMessageTemplate(path, kind, messages[kind]); err != nil {
			return err
		}
	}
	return nil
}

// validateIntegrationMessages checks the templates of an integration against the catalogue, Discord's limits, and
// its server associations.
func validateIntegrationMessages(i DiscordIntegration) error {
	if len(i.Messages) == 0 && len(i.ServerMessages) == 0 {
		return nil
	}
	if normalizedProvider(i.Provider) != providerDiscord {
		return messageFieldError("messages", "message templates are only supported for Discord integrations")
	}
	if err := validateMessageSet("messages", i.Messages); err != nil {
		return err
	}
	ids := make([]string, 0, len(i.ServerMessages))
	for id := range i.ServerMessages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !slices.Contains(i.ServerIDs, id) {
			return messageFieldError("serverMessages."+id, "server %q is not associated with this integration", id)
		}
		if err := validateMessageSet("serverMessages."+id, i.ServerMessages[id]); err != nil {
			return err
		}
	}
	return nil
}

// normalizeMessages fills omitted colors and drops every entry that equals what it would fall back to: the
// built-in default for the integration level, the integration's effective template for a server override. Stored
// state therefore holds only real customizations.
func (i *DiscordIntegration) normalizeMessages() {
	normalize := func(messages map[EventKind]MessageTemplate, fallback func(EventKind) MessageTemplate) map[EventKind]MessageTemplate {
		out := map[EventKind]MessageTemplate{}
		for kind, tmpl := range messages {
			tmpl = tmpl.clone()
			if tmpl.Color == nil {
				color := defaultMessageTemplate(kind).Color
				tmpl.Color = color
			}
			if tmpl.Enabled != nil && *tmpl.Enabled {
				tmpl.Enabled = nil
			}
			if !tmpl.equal(fallback(kind)) {
				out[kind] = tmpl
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	i.Messages = normalize(i.Messages, defaultMessageTemplate)
	servers := map[string]map[EventKind]MessageTemplate{}
	for id, messages := range i.ServerMessages {
		if kept := normalize(messages, func(kind EventKind) MessageTemplate { return i.integrationTemplate(kind) }); kept != nil {
			servers[id] = kept
		}
	}
	if len(servers) == 0 {
		servers = nil
	}
	i.ServerMessages = servers
}

func (i DiscordIntegration) cloneMessages() DiscordIntegration {
	if i.Messages != nil {
		messages := make(map[EventKind]MessageTemplate, len(i.Messages))
		for kind, tmpl := range i.Messages {
			messages[kind] = tmpl.clone()
		}
		i.Messages = messages
	}
	if i.ServerMessages != nil {
		servers := make(map[string]map[EventKind]MessageTemplate, len(i.ServerMessages))
		for id, set := range i.ServerMessages {
			messages := make(map[EventKind]MessageTemplate, len(set))
			for kind, tmpl := range set {
				messages[kind] = tmpl.clone()
			}
			servers[id] = messages
		}
		i.ServerMessages = servers
	}
	return i
}

// integrationTemplate is the template the integration applies to servers without their own override.
func (i DiscordIntegration) integrationTemplate(kind EventKind) MessageTemplate {
	if tmpl, ok := i.Messages[kind]; ok {
		return tmpl
	}
	return defaultMessageTemplate(kind)
}

// resolveMessage applies the order server override, integration template, built-in default.
func (i DiscordIntegration) resolveMessage(serverID string, kind EventKind) EffectiveMessage {
	if serverID != "" {
		if tmpl, ok := i.ServerMessages[serverID][kind]; ok {
			return EffectiveMessage{tmpl.clone(), messageSourceServer}
		}
	}
	if tmpl, ok := i.Messages[kind]; ok {
		return EffectiveMessage{tmpl.clone(), messageSourceIntegration}
	}
	return EffectiveMessage{defaultMessageTemplate(kind), messageSourceDefault}
}

// messageEnabled reports whether a real event of the kind should produce a Discord message for the server.
func (i DiscordIntegration) messageEnabled(serverID string, kind EventKind) bool {
	if normalizedProvider(i.Provider) != providerDiscord {
		return true
	}
	return i.resolveMessage(serverID, kind).enabled()
}

type integrationView struct {
	DiscordIntegration
	EffectiveMessages       map[EventKind]EffectiveMessage            `json:"effectiveMessages"`
	EffectiveServerMessages map[string]map[EventKind]EffectiveMessage `json:"effectiveServerMessages"`
}

func newIntegrationView(i DiscordIntegration) integrationView {
	view := integrationView{DiscordIntegration: i, EffectiveMessages: map[EventKind]EffectiveMessage{}, EffectiveServerMessages: map[string]map[EventKind]EffectiveMessage{}}
	if normalizedProvider(i.Provider) != providerDiscord {
		return view
	}
	for _, spec := range discordMessageSpecs {
		view.EffectiveMessages[spec.Kind] = i.resolveMessage("", spec.Kind)
	}
	for _, id := range i.ServerIDs {
		effective := map[EventKind]EffectiveMessage{}
		for _, spec := range discordMessageSpecs {
			effective[spec.Kind] = i.resolveMessage(id, spec.Kind)
		}
		view.EffectiveServerMessages[id] = effective
	}
	return view
}

type messageDefaultView struct {
	Label    string               `json:"label"`
	Server   bool                 `json:"server"`
	Title    string               `json:"title"`
	Desc     string               `json:"description"`
	Color    int                  `json:"color"`
	Enabled  bool                 `json:"enabled"`
	Variants []messageVariantView `json:"variants,omitempty"`
}

type messageVariantView struct {
	When        string `json:"when"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type messageCatalogue struct {
	Defaults  map[EventKind]messageDefaultView `json:"messageDefaults"`
	Variables map[EventKind][]messageVariable  `json:"messageVariables"`
	Limits    map[string]int                   `json:"messageLimits"`
}

func buildMessageCatalogue() messageCatalogue {
	catalogue := messageCatalogue{Defaults: map[EventKind]messageDefaultView{}, Variables: map[EventKind][]messageVariable{}, Limits: map[string]int{"title": discordTitleLimit, "description": discordDescriptionLimit, "total": discordEmbedTotalLimit, "colorMax": discordColorMax}}
	for _, spec := range discordMessageSpecs {
		view := messageDefaultView{Label: spec.Label, Server: spec.Server, Title: spec.Title, Desc: spec.Description, Color: spec.Color, Enabled: true}
		for _, variant := range spec.Variants {
			view.Variants = append(view.Variants, messageVariantView{variant.When, variant.Title, variant.Description})
		}
		catalogue.Defaults[spec.Kind] = view
		catalogue.Variables[spec.Kind] = messageKindVariables(spec.Kind)
	}
	return catalogue
}
