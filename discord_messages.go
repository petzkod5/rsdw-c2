package main

import (
	"errors"
	"time"
)

type discordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Color       int                 `json:"color"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
	Footer      discordEmbedFooter  `json:"footer"`
	Timestamp   string              `json:"timestamp,omitempty"`
}

type discordEmbedField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type discordEmbedFooter struct {
	Text string `json:"text"`
}

type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

type discordMessage struct {
	Embeds          []discordEmbed         `json:"embeds"`
	Nonce           string                 `json:"nonce"`
	EnforceNonce    bool                   `json:"enforce_nonce"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
}

type discordMessageSpec struct {
	Kind        EventKind
	Label       string
	Title       string
	Description string
	Color       int
	Server      bool
	// Variants replace the built-in title and description when the event carries evidence that makes a more
	// specific message possible. They apply unless a template changes the built-in title or description.
	Variants []discordMessageVariant
}

type discordMessageVariant struct {
	When        string
	Match       func(Event) bool
	Title       string
	Description string
}

var discordMessageSpecs = []discordMessageSpec{
	{Kind: MemoryPressureRestartRequested, Label: "Memory pressure restart requested", Title: "Memory pressure restart requested", Description: "The server kept hogging memory. C2 requested a restart before it ate the whole damn limit.", Color: 0xF59E0B, Server: true},
	{Kind: RestartWarning, Label: "Restart warning", Title: "Restart approaching", Description: "A scheduled restart is approaching. Save your progress.", Color: 0xF59E0B, Server: true, Variants: []discordMessageVariant{{"a restart warning with scheduled-restart evidence", func(event Event) bool { return event.Evidence.RestartWarning != nil }, "Restart approaching", "Restart in {{warningMinutes}} minutes. Save your progress. Scheduled for {{scheduledTime}}."}}},
	{Kind: RestartRequested, Label: "Restart requested", Title: "Restart requested", Description: "The server was told to get its act together. It chose a dramatic reboot instead.", Color: 0xF59E0B, Server: true},
	{Kind: RestartCompleted, Label: "Restart completed", Title: "Restart completed", Description: "The corpse has staggered back online. It did the bare minimum and expects a fucking parade.", Color: 0x22C55E, Server: true},
	{Kind: RestartFailed, Label: "Restart failed", Title: "Restart not confirmed", Description: "The restart fucked off into the void and never bothered to report back. Even the server can't explain what the hell it's doing.", Color: 0xEF4444, Server: true},
	{Kind: PlayerJoined, Label: "Player joined", Title: "Player count increased", Description: "Another idiot crawled into this rancid little server. The population is up; the average brain-cell count remains terminal.", Color: 0x3B82F6, Server: true, Variants: []discordMessageVariant{{"a fresh roster names the joined players", func(event Event) bool { return len(event.Evidence.JoinedPlayers) > 0 }, "Player joined", "{{joinedPlayers}} joined the server."}}},
	{Kind: PlayerLimitReached, Label: "Player limit reached", Title: "Player limit reached", Description: "It's full. Stop cramming people into this overcrowded shit-casket before it bursts and sprays everyone's problems across Discord.", Color: 0xF59E0B, Server: true},
	{Kind: ServerDown, Label: "Server down", Title: "Server unhealthy", Description: "The server has stopped pretending to be functional. How honest of it.", Color: 0xEF4444, Server: true},
	{Kind: ServerRecovered, Label: "Server recovered", Title: "Server recovered", Description: "It is back. Nobody knows why, and nobody should trust it.", Color: 0x22C55E, Server: true},
	{Kind: ServerStopped, Label: "Server stopped", Title: "Server stopped", Description: "The operator parked this world. The volume is still here. The players are not.", Color: 0xF59E0B, Server: true},
	{Kind: ServerStarted, Label: "Server started", Title: "Server started", Description: "Same world, same id, same disk. Try not to immediately fill it with tragedy.", Color: 0x22C55E, Server: true},
	{Kind: BackupStarted, Label: "Backup started", Title: "Backup started", Description: "C2 is collecting the configured backup profile.", Color: 0x3B82F6, Server: true},
	{Kind: BackupCompleted, Label: "Backup completed", Title: "Backup completed", Description: "The backup bundle is stored and ready to download.", Color: 0x22C55E, Server: true},
	{Kind: BackupFailed, Label: "Backup failed", Title: "Backup failed", Description: "No completed backup was published. Review the failure in C2.", Color: 0xEF4444, Server: true},
	{Kind: IntegrationTest, Label: "Integration test", Title: "Discord integration test", Description: "The bot successfully vomited into Discord and called it a test. The webhook works; civilization remains a mistake.", Color: 0x8B5CF6, Server: false},
}

var errUnsupportedDiscordMessage = errors.New("Discord message kind is unsupported or unavailable")

func discordSpec(kind EventKind) (discordMessageSpec, bool) {
	for _, spec := range discordMessageSpecs {
		if spec.Kind == kind {
			return spec, true
		}
	}
	return discordMessageSpec{}, false
}

func defaultMessageTemplate(kind EventKind) MessageTemplate {
	spec, _ := discordSpec(kind)
	color := spec.Color
	return MessageTemplate{Title: spec.Title, Description: spec.Description, Color: &color}
}

// renderDiscordEmbed renders the event with tmpl, or with the built-in default (including its evidence
// variants) when tmpl is nil. The Server and Players fields, footer, and timestamp do not depend on the template.
func renderDiscordEmbed(event Event, tmpl *MessageTemplate) (discordEmbed, error) {
	spec, ok := discordSpec(event.Kind)
	if !ok {
		return discordEmbed{}, errUnsupportedDiscordMessage
	}
	title, description, color := spec.Title, spec.Description, spec.Color
	// A template that keeps the built-in title and description (a color-only customization) still gets the
	// evidence variant. Only a template that changes the text replaces it.
	if tmpl == nil || (tmpl.Title == spec.Title && tmpl.Description == spec.Description) {
		for _, variant := range spec.Variants {
			if variant.Match(event) {
				title, description = variant.Title, variant.Description
				break
			}
		}
	} else {
		title, description = tmpl.Title, tmpl.Description
	}
	if tmpl != nil && tmpl.Color != nil {
		color = *tmpl.Color
	}
	values := messageVariableValues(event)
	embed := discordEmbed{Title: expandTemplate(title, values), Description: expandTemplate(description, values), Color: color, Footer: discordEmbedFooter{Text: "Dragonwilds C2"}}
	if spec.Server {
		embed.Fields = []discordEmbedField{{Name: "Server", Value: values["server"]}}
	}
	if !event.Timestamp.IsZero() {
		embed.Timestamp = event.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	if count := event.Evidence.PlayerCount; count != nil {
		embed.Fields = append(embed.Fields, discordEmbedField{Name: "Players", Value: playerCountText(count)})
	}
	return fitEmbed(embed), nil
}

func buildDiscordMessage(delivery Delivery) (discordMessage, error) {
	embed, err := renderDiscordEmbed(delivery.Event, delivery.Template)
	if err != nil {
		return discordMessage{}, err
	}
	return discordMessage{Embeds: []discordEmbed{embed}, Nonce: delivery.ID, EnforceNonce: true, AllowedMentions: discordAllowedMentions{Parse: []string{}}}, nil
}

type discordDeliveryView struct {
	Delivery
	Embed *discordEmbed `json:"embed,omitempty"`
}

func discordDeliveryViews(deliveries []Delivery) []discordDeliveryView {
	views := make([]discordDeliveryView, 0, len(deliveries))
	for _, delivery := range deliveries {
		view := discordDeliveryView{Delivery: delivery}
		if normalizedProvider(delivery.Provider) == providerDiscord {
			if embed, err := renderDiscordEmbed(delivery.Event, delivery.Template); err == nil {
				view.Embed = &embed
			}
		}
		views = append(views, view)
	}
	return views
}
