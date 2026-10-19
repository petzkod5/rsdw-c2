package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func templateFixture(title, description string, color int) MessageTemplate {
	return MessageTemplate{Title: title, Description: description, Color: intPtr(color)}
}

func templateIntegration() DiscordIntegration {
	i := integrationFixture()
	i.ServerIDs = []string{"scuffedtards", "other"}
	return i
}

func decodeBody[T any](t *testing.T, body string) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err, body)
	}
	return out
}

func TestMessageResolutionOrder(t *testing.T) {
	i := templateIntegration()
	i.Messages = map[EventKind]MessageTemplate{RestartCompleted: templateFixture("Integration title", "Integration body", 0x111111)}
	i.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {RestartCompleted: templateFixture("Server title", "Server body", 0x222222), ServerDown: {Title: "Down", Description: "Off", Color: intPtr(1), Enabled: boolPtr(false)}}}
	cases := []struct {
		server  string
		kind    EventKind
		title   string
		source  messageSource
		enabled bool
	}{
		{"scuffedtards", RestartCompleted, "Server title", messageSourceServer, true},
		{"other", RestartCompleted, "Integration title", messageSourceIntegration, true},
		{"", RestartCompleted, "Integration title", messageSourceIntegration, true},
		{"scuffedtards", RestartFailed, "Restart not confirmed", messageSourceDefault, true},
		{"scuffedtards", ServerDown, "Down", messageSourceServer, false},
		{"other", ServerDown, "Server unhealthy", messageSourceDefault, true},
	}
	for _, tc := range cases {
		got := i.resolveMessage(tc.server, tc.kind)
		if got.Title != tc.title || got.Source != tc.source || got.enabled() != tc.enabled {
			t.Errorf("resolve(%q, %s) = %+v", tc.server, tc.kind, got)
		}
		if i.messageEnabled(tc.server, tc.kind) != tc.enabled {
			t.Errorf("messageEnabled(%q, %s)", tc.server, tc.kind)
		}
	}
	if got := i.resolveMessage("scuffedtards", RestartFailed); *got.Color != 0xEF4444 {
		t.Fatalf("default color = %x", *got.Color)
	}
}

func TestMessageVariablesRenderEveryVariable(t *testing.T) {
	at := time.Date(2026, 1, 1, 7, 0, 0, 0, time.FixedZone("EST", -5*60*60))
	full := Event{Kind: PlayerJoined, ServerName: "Example server", Timestamp: at, Image: "registry.example:5000/dragonwilds/server:v1.2.3",
		Evidence: AlertEvidence{
			PlayerCount:    &PlayerCountEvidence{Available: true, Players: 3, MaxPlayers: 10},
			JoinedPlayers:  []ConnectedPlayer{{Name: "Sam", CharacterName: "Brynn"}, {Name: "", CharacterName: "Tor"}},
			RestartWarning: &RestartWarningEvidence{Trigger: "scheduled", RestartAt: time.Date(2026, 1, 1, 13, 5, 0, 0, time.UTC), Minutes: 5},
		}}
	cases := []struct {
		name, variable string
		event          Event
		want           string
	}{
		{"event", "event", full, "Player joined"},
		{"server", "server", full, "Example server"},
		{"players", "players", full, "3"},
		{"maxPlayers", "maxPlayers", full, "10"},
		{"player", "player", full, "Sam"},
		{"character", "character", full, "Brynn, Tor"},
		{"joinedPlayers", "joinedPlayers", full, "Brynn (Sam), Tor"},
		{"time", "time", full, "2026-01-01T12:00:00Z"},
		{"scheduledTime", "scheduledTime", full, "2026-01-01T13:05:00Z"},
		{"warningMinutes", "warningMinutes", full, "5"},
		{"image", "image", full, "registry.example:5000/dragonwilds/server:v1.2.3"},
		{"imageTag", "imageTag", full, "v1.2.3"},
		{"spaces", " server ", full, "Example server"},
		{"missing server", "server", Event{Kind: ServerDown}, "Unknown server"},
		{"missing time", "time", Event{Kind: ServerDown}, "unavailable"},
		{"missing count", "players", Event{Kind: ServerDown}, "unavailable"},
		{"unavailable count", "players", Event{Kind: ServerRecovered, Evidence: AlertEvidence{PlayerCount: &PlayerCountEvidence{MaxPlayers: 4}}}, "unavailable"},
		{"limit with unavailable count", "maxPlayers", Event{Kind: ServerRecovered, Evidence: AlertEvidence{PlayerCount: &PlayerCountEvidence{MaxPlayers: 4}}}, "4"},
		{"missing max", "maxPlayers", Event{Kind: ServerDown}, "unavailable"},
		{"missing player", "player", Event{Kind: ServerDown}, "unavailable"},
		{"character without account", "player", Event{Kind: PlayerJoined, Evidence: AlertEvidence{JoinedPlayers: []ConnectedPlayer{{CharacterName: "Tor"}}}}, "unavailable"},
		{"missing character", "character", Event{Kind: ServerDown}, "unavailable"},
		{"missing joined", "joinedPlayers", Event{Kind: ServerDown}, "unavailable"},
		{"missing schedule", "scheduledTime", Event{Kind: ServerDown}, "unavailable"},
		{"missing minutes", "warningMinutes", Event{Kind: ServerDown}, "unavailable"},
		{"missing image", "image", Event{Kind: ServerDown}, "unavailable"},
		{"missing tag", "imageTag", Event{Kind: ServerDown}, "unavailable"},
		{"untagged image", "imageTag", Event{Kind: ServerDown, Image: "registry.example:5000/server"}, "unavailable"},
		{"digest image", "imageTag", Event{Kind: ServerDown, Image: "registry.example/server:v2@sha256:abc"}, "v2"},
	}
	covered := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			covered[strings.TrimSpace(tc.variable)] = true
			tmpl := templateFixture("[{{"+tc.variable+"}}]", "<{{"+tc.variable+"}}>", 0)
			if tc.event.Kind == "" {
				tc.event.Kind = ServerDown
			}
			embed, err := renderDiscordEmbed(tc.event, &tmpl)
			if err != nil {
				t.Fatal(err)
			}
			if embed.Title != "["+tc.want+"]" || embed.Description != "<"+tc.want+">" {
				t.Fatalf("embed = %q / %q", embed.Title, embed.Description)
			}
		})
	}
	for _, variable := range messageVariableCatalogue {
		if !covered[variable.Name] {
			t.Errorf("variable %s has no rendering test", variable.Name)
		}
	}
}

func TestMessageRendererIsSinglePassAndLiteral(t *testing.T) {
	event := Event{Kind: ServerDown, ServerName: "{{time}} {{player}}", Timestamp: time.Unix(0, 0)}
	tmpl := templateFixture("{{server}}", "{{ server }} {{ not closed {{}} {single} {{{{server}}}}", 5)
	embed, err := renderDiscordEmbed(event, &tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if embed.Title != "{{time}} {{player}}" {
		t.Fatalf("values were expanded again: %q", embed.Title)
	}
	if !strings.HasPrefix(embed.Description, "{{time}} {{player}} {{ not closed {{}} {single} ") {
		t.Fatalf("literal text changed: %q", embed.Description)
	}
	if *tmpl.Color != 5 || embed.Color != 5 {
		t.Fatal("color not applied")
	}
	if name, unknown := unknownPlaceholder("{{}}"); !unknown || name != "" {
		t.Fatal("empty placeholder accepted")
	}
	if _, unknown := unknownPlaceholder("{{ server }} {single} {{ not closed"); unknown {
		t.Fatal("literal text flagged")
	}
}

func TestRenderKeepsServerFieldFooterTimestampAndMentionsOff(t *testing.T) {
	event := Event{Kind: RestartCompleted, ServerName: "Example server", Timestamp: time.Date(2026, 1, 1, 7, 0, 0, 0, time.FixedZone("EST", -5*60*60)), Evidence: AlertEvidence{PlayerCount: &PlayerCountEvidence{Available: true, Players: 2, MaxPlayers: 8}}}
	tmpl := templateFixture("@everyone {{event}}", "custom", 0x123456)
	message, err := buildDiscordMessage(Delivery{ID: "d", Event: event, Template: &tmpl})
	if err != nil {
		t.Fatal(err)
	}
	want := discordEmbed{Title: "@everyone Restart completed", Description: "custom", Color: 0x123456, Fields: []discordEmbedField{{"Server", "Example server"}, {"Players", "2/8"}}, Footer: discordEmbedFooter{Text: "Dragonwilds C2"}, Timestamp: "2026-01-01T12:00:00Z"}
	if !reflect.DeepEqual(message.Embeds[0], want) || len(message.AllowedMentions.Parse) != 0 || message.AllowedMentions.Parse == nil {
		t.Fatalf("message = %+v", message)
	}
	test := templateFixture("t", "d", 1)
	embed, _ := renderDiscordEmbed(Event{Kind: IntegrationTest, ServerName: "Must not appear"}, &test)
	if len(embed.Fields) != 0 {
		t.Fatalf("integration test embed has fields: %+v", embed.Fields)
	}
}

func TestDefaultVariantsKeepTodaysText(t *testing.T) {
	warning := Event{Kind: RestartWarning, ServerName: "S", Evidence: AlertEvidence{RestartWarning: &RestartWarningEvidence{Minutes: 5, RestartAt: time.Date(2026, 1, 1, 13, 5, 0, 0, time.UTC)}}}
	embed, _ := renderDiscordEmbed(warning, nil)
	if embed.Title != "Restart approaching" || embed.Description != "Restart in 5 minutes. Save your progress. Scheduled for 2026-01-01T13:05:00Z." {
		t.Fatalf("warning = %+v", embed)
	}
	joined := Event{Kind: PlayerJoined, Evidence: AlertEvidence{JoinedPlayers: []ConnectedPlayer{{Name: "Sam", CharacterName: "Brynn"}, {CharacterName: "Tor"}}}}
	embed, _ = renderDiscordEmbed(joined, nil)
	if embed.Title != "Player joined" || embed.Description != "Brynn (Sam), Tor joined the server." {
		t.Fatalf("joined = %+v", embed)
	}
	// A customized template wins even when evidence would select a default variant.
	custom := templateFixture("Custom", "Custom body", 1)
	embed, _ = renderDiscordEmbed(joined, &custom)
	if embed.Title != "Custom" || embed.Description != "Custom body" {
		t.Fatalf("custom = %+v", embed)
	}
}

func TestRenderTruncatesToDiscordLimits(t *testing.T) {
	long := strings.Repeat("é", 5000)
	event := Event{Kind: PlayerJoined, ServerName: long, Evidence: AlertEvidence{PlayerCount: &PlayerCountEvidence{Available: true, Players: 1, MaxPlayers: 2}}}
	tmpl := templateFixture("{{server}}", "{{server}}", 0)
	embed, err := renderDiscordEmbed(event, &tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(embed.Title)); n != discordTitleLimit || !strings.HasSuffix(embed.Title, "…") {
		t.Fatalf("title runes = %d", n)
	}
	if n := len([]rune(embed.Description)); n > discordDescriptionLimit {
		t.Fatalf("description runes = %d", n)
	}
	if total := embedTotalLength(embed); total > discordEmbedTotalLimit {
		t.Fatalf("total = %d", total)
	}
	if n := len([]rune(embed.Fields[0].Value)); n != discordFieldValueLimit {
		t.Fatalf("field value runes = %d", n)
	}
	field := discordEmbedField{Name: "n", Value: strings.Repeat("c", 1024)}
	fitted := fitEmbed(discordEmbed{Title: strings.Repeat("a", 256), Description: strings.Repeat("b", 4096), Fields: []discordEmbedField{field, field, field, field}, Footer: discordEmbedFooter{Text: "f"}})
	if want := 4096 - (256 + 4096 + 4*1025 + 1 - discordEmbedTotalLimit); len([]rune(fitted.Description)) != want || embedTotalLength(fitted) != discordEmbedTotalLimit {
		t.Fatalf("fit total = %d, description = %d", embedTotalLength(fitted), len([]rune(fitted.Description)))
	}
}

func TestMessageValidation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	cases := []struct {
		name    string
		edit    func(*DiscordIntegration)
		field   string
		message []string
	}{
		{"unknown variable", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{RestartFailed: templateFixture("ok", "Hello {{secretToken}}", 1)}
		}, "messages.restart_failed.description", []string{"restart_failed", "{{secretToken}}"}},
		{"unknown variable in title", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("{{provider}}", "ok", 1)}
		}, "messages.server_down.title", []string{"server_down", "{{provider}}"}},
		{"unknown variable in server override", func(i *DiscordIntegration) {
			i.ServerMessages = map[string]map[EventKind]MessageTemplate{"other": {ServerDown: templateFixture("ok", "{{nope}}", 1)}}
		}, "serverMessages.other.server_down.description", []string{"server_down", "{{nope}}"}},
		{"unknown kind", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{"made_up": templateFixture("a", "b", 1)}
		}, "messages.made_up", []string{"made_up"}},
		{"rule only kind", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{RebootScheduleChanged: templateFixture("a", "b", 1)}
		}, "messages.reboot_schedule_changed", []string{"reboot_schedule_changed"}},
		{"unknown kind in server override", func(i *DiscordIntegration) {
			i.ServerMessages = map[string]map[EventKind]MessageTemplate{"other": {"made_up": templateFixture("a", "b", 1)}}
		}, "serverMessages.other.made_up", []string{"made_up"}},
		{"title too long", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture(long(257), "b", 1)}
		}, "messages.server_down.title", []string{"257", "256"}},
		{"description too long", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("a", long(4097), 1)}
		}, "messages.server_down.description", []string{"4097", "4096"}},
		{"color too large", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("a", "b", 0x1000000)}
		}, "messages.server_down.color", []string{"color"}},
		{"color negative", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("a", "b", -1)}
		}, "messages.server_down.color", []string{"color"}},
		{"empty message", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture(" ", "", 1)}
		}, "messages.server_down.title", []string{"title or a description"}},
		{"title line break", func(i *DiscordIntegration) {
			i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("a\nb", "b", 1)}
		}, "messages.server_down.title", []string{"line breaks"}},
		{"unassociated server", func(i *DiscordIntegration) {
			i.ServerMessages = map[string]map[EventKind]MessageTemplate{"stranger": {ServerDown: templateFixture("a", "b", 1)}}
		}, "serverMessages.stranger", []string{"stranger", "not associated"}},
		{"webhook provider", func(i *DiscordIntegration) {
			i.Provider, i.GuildID, i.ChannelID, i.WebhookURL = providerWebhook, "", "", "https://hooks.example.com/rsdw"
			i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("a", "b", 1)}
		}, "messages", []string{"Discord"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := templateIntegration()
			tc.edit(&i)
			err := validateIntegrationMessages(i)
			if err == nil {
				t.Fatal("accepted")
			}
			fields := integrationErrorFields(err)
			if len(fields) != 1 || fields[tc.field] == "" {
				t.Fatalf("fields = %v, want %s", fields, tc.field)
			}
			for _, fragment := range tc.message {
				if !strings.Contains(err.Error(), fragment) {
					t.Fatalf("error %q lacks %q", err, fragment)
				}
			}
		})
	}
	valid := templateIntegration()
	valid.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture(long(256), long(4096), 0xFFFFFF), IntegrationTest: templateFixture("{{event}} {{ server }}", "multi\nline {{event}}", 0)}
	valid.ServerMessages = map[string]map[EventKind]MessageTemplate{"other": {RestartWarning: templateFixture("a", "{{warningMinutes}} {{scheduledTime}}", 7)}}
	if err := validateIntegrationMessages(valid); err != nil {
		t.Fatal(err)
	}
}

func TestExistingFieldErrorMappingStillWorks(t *testing.T) {
	state := State{Servers: map[string]Server{"scuffedtards": {ID: "scuffedtards"}}}
	i := integrationFixture()
	i.Name = ""
	if fields := integrationErrorFields(validateIntegration(i, state)); fields["name"] == "" {
		t.Fatalf("fields = %v", fields)
	}
	i = integrationFixture()
	i.SecretRef.Name = "Not Valid"
	if fields := integrationErrorFields(validateIntegration(i, state)); fields["secretName"] == "" || fields["secretKey"] == "" {
		t.Fatalf("fields = %v", fields)
	}
	i = integrationFixture()
	i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("{{secretRef}}", "b", 1)}
	if fields := integrationErrorFields(validateIntegration(i, state)); len(fields) != 1 || fields["messages.server_down.title"] == "" {
		t.Fatalf("template errors must not be mapped to other fields: %v", fields)
	}
}

func TestNormalizeOmitsDefaults(t *testing.T) {
	def := defaultMessageTemplate(ServerDown)
	i := templateIntegration()
	i.Messages = map[EventKind]MessageTemplate{
		ServerDown:      {Title: def.Title, Description: def.Description},                                       // equals default, color omitted
		ServerRecovered: {Title: "Recovered", Description: "Back", Enabled: boolPtr(true)},                      // custom, color omitted
		RestartFailed:   {Title: "x", Description: "y", Color: intPtr(1), Enabled: boolPtr(false)},              // disabled is a customization
		BackupFailed:    {Title: defaultMessageTemplate(BackupFailed).Title, Description: "", Color: intPtr(0)}, // differs
	}
	i.ServerMessages = map[string]map[EventKind]MessageTemplate{
		"scuffedtards": {
			ServerRecovered: templateFixture("Recovered", "Back", *defaultMessageTemplate(ServerRecovered).Color), // equals integration template
			ServerDown:      templateFixture(def.Title, def.Description, *def.Color),                              // equals default; no integration override, so dropped
			RestartFailed:   templateFixture("x", "y", 1),                                                         // differs from the disabled integration entry only by enabled
		},
		"other": {ServerRecovered: templateFixture("Other", "Back", 2)},
		"gone":  {},
	}
	i.normalizeMessages()
	if _, ok := i.Messages[ServerDown]; ok {
		t.Fatal("entry equal to the default was stored")
	}
	if got := i.Messages[ServerRecovered]; got.Color == nil || *got.Color != *defaultMessageTemplate(ServerRecovered).Color || got.Enabled != nil {
		t.Fatalf("recovered = %+v", got)
	}
	if got, ok := i.Messages[RestartFailed]; !ok || got.enabled() {
		t.Fatalf("disabled entry lost: %+v", got)
	}
	if _, ok := i.Messages[BackupFailed]; !ok || len(i.Messages) != 3 {
		t.Fatalf("messages = %+v", i.Messages)
	}
	servers := i.ServerMessages
	if _, ok := servers["scuffedtards"][ServerRecovered]; ok {
		t.Fatal("server override equal to the integration template was stored")
	}
	if _, ok := servers["scuffedtards"][ServerDown]; ok {
		t.Fatal("server override equal to the default was stored")
	}
	if _, ok := servers["scuffedtards"][RestartFailed]; !ok {
		t.Fatal("server override that re-enables a disabled message was dropped")
	}
	if _, ok := servers["gone"]; ok || len(servers) != 2 {
		t.Fatalf("servers = %+v", servers)
	}
	// A server override equal to the built-in default must survive when the integration customizes the kind.
	j := templateIntegration()
	j.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("Integration", "Custom", 1)}
	j.ServerMessages = map[string]map[EventKind]MessageTemplate{"other": {ServerDown: templateFixture(def.Title, def.Description, *def.Color)}}
	j.normalizeMessages()
	if got := j.resolveMessage("other", ServerDown); got.Source != messageSourceServer || got.Title != def.Title {
		t.Fatalf("reset-to-default override lost: %+v", got)
	}
	empty := templateIntegration()
	empty.Messages = map[EventKind]MessageTemplate{ServerDown: {Title: def.Title, Description: def.Description}}
	empty.normalizeMessages()
	if empty.Messages != nil || empty.ServerMessages != nil {
		t.Fatal("empty maps must be nil so they are omitted from state")
	}
	data, _ := json.Marshal(empty)
	if strings.Contains(string(data), "messages") || strings.Contains(string(data), "serverMessages") {
		t.Fatalf("state = %s", data)
	}
}

func saveIntegration(t *testing.T, app *App, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	if i, ok := body.(DiscordIntegration); ok {
		i.ID = ""
		body = i
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return requestJSON(t, app, method, path, string(data))
}

// saveEmptying PUTs the integration with the named template keys set to {}. encoding/json omits empty maps, and
// the API treats an absent key as "keep what is stored", so clearing needs the key spelled out.
func saveEmptying(t *testing.T, app *App, path string, i DiscordIntegration, keys ...string) *httptest.ResponseRecorder {
	t.Helper()
	i.ID = ""
	data, err := json.Marshal(i)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSuffix(string(data), "}")
	for _, key := range keys {
		body += `,"` + key + `":{}`
	}
	return requestJSON(t, app, "PUT", path, body+"}")
}

func TestIntegrationAPIMessages(t *testing.T) {
	app := newTestApp(t, true)
	input := templateIntegration()
	input.ID = ""
	input.ServerIDs = []string{"scuffedtards"}
	input.Messages = map[EventKind]MessageTemplate{
		RestartCompleted: {Title: "Back!", Description: "{{server}} is up on {{imageTag}}", Color: intPtr(0xABCDEF)},
		ServerDown:       {Title: defaultMessageTemplate(ServerDown).Title, Description: defaultMessageTemplate(ServerDown).Description},
	}
	input.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {PlayerJoined: {Title: "Hi", Description: "{{joinedPlayers}}", Color: intPtr(3), Enabled: boolPtr(false)}}}
	res := saveIntegration(t, app, "POST", "/api/integrations", input)
	if res.Code != 201 {
		t.Fatal(res.Code, res.Body.String())
	}
	created := decodeBody[DiscordIntegration](t, res.Body.String())
	if !reflect.DeepEqual(created, app.store.Snapshot().Integrations[created.ID]) {
		t.Fatalf("response differs from the stored integration: %+v", created)
	}
	stored := app.store.Snapshot().Integrations[created.ID]
	if len(stored.Messages) != 1 || stored.Messages[RestartCompleted].Title != "Back!" || len(stored.ServerMessages["scuffedtards"]) != 1 {
		t.Fatalf("stored = %+v", stored)
	}

	listed := requestJSON(t, app, "GET", "/api/integrations", "")
	var list struct {
		Integrations     []integrationView                `json:"integrations"`
		MessageDefaults  map[EventKind]messageDefaultView `json:"messageDefaults"`
		MessageVariables map[EventKind][]messageVariable  `json:"messageVariables"`
		MessageLimits    map[string]int                   `json:"messageLimits"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	view := list.Integrations[0]
	if got := view.EffectiveServerMessages["scuffedtards"]; got[PlayerJoined].Source != messageSourceServer || got[PlayerJoined].enabled() || got[RestartCompleted].Source != messageSourceIntegration || got[RestartFailed].Source != messageSourceDefault {
		t.Fatalf("effective server messages = %+v", got)
	}
	if len(view.EffectiveMessages) != len(discordMessageSpecs) || view.EffectiveMessages[PlayerJoined].Source != messageSourceDefault {
		t.Fatalf("effective messages = %d", len(view.EffectiveMessages))
	}
	if !strings.Contains(listed.Body.String(), `"source":"server"`) || !strings.Contains(listed.Body.String(), `"messages":{"restart_completed"`) {
		t.Fatal(listed.Body.String())
	}
	for _, spec := range discordMessageSpecs {
		def := list.MessageDefaults[spec.Kind]
		if def.Title != spec.Title || def.Desc != spec.Description || def.Color != spec.Color || def.Label == "" || !def.Enabled || def.Server != spec.Server {
			t.Fatalf("default for %s = %+v", spec.Kind, def)
		}
		if len(list.MessageVariables[spec.Kind]) == 0 {
			t.Fatalf("no variables for %s", spec.Kind)
		}
	}
	if list.MessageLimits["title"] != 256 || list.MessageLimits["description"] != 4096 || list.MessageLimits["total"] != 6000 || list.MessageLimits["colorMax"] != 0xFFFFFF {
		t.Fatalf("limits = %v", list.MessageLimits)
	}
	if len(list.MessageDefaults[PlayerJoined].Variants) != 1 || len(list.MessageDefaults[RestartWarning].Variants) != 1 {
		t.Fatal("default variants are not exposed")
	}
	names := func(kind EventKind) []string {
		var out []string
		for _, v := range list.MessageVariables[kind] {
			out = append(out, v.Name)
		}
		return out
	}
	if !reflect.DeepEqual(names(RestartWarning), []string{"event", "server", "time", "scheduledTime", "warningMinutes", "image", "imageTag"}) || !reflect.DeepEqual(names(IntegrationTest), []string{"event", "server", "time", "image", "imageTag"}) {
		t.Fatalf("variables = %v / %v", names(RestartWarning), names(IntegrationTest))
	}

	// An update without the template keys, like the legacy form, keeps the stored templates.
	update := templateIntegration()
	update.Name = "Renamed"
	update.ServerIDs = []string{"scuffedtards"}
	if res := saveIntegration(t, app, "PUT", "/api/integrations/"+created.ID, update); res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	kept := app.store.Snapshot().Integrations[created.ID]
	if kept.Name != "Renamed" || len(kept.Messages) != 1 || len(kept.ServerMessages) != 1 {
		t.Fatalf("legacy update lost templates: %+v", kept)
	}
	// Explicit empty objects clear them.
	if res := saveEmptying(t, app, "/api/integrations/"+created.ID, update, "messages", "serverMessages"); res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	cleared := app.store.Snapshot().Integrations[created.ID]
	if cleared.Messages != nil || cleared.ServerMessages != nil {
		t.Fatalf("not cleared: %+v", cleared)
	}
	raw, _ := os.ReadFile(app.store.path)
	if strings.Contains(string(raw), `"messages"`) || strings.Contains(string(raw), "serverMessages") {
		t.Fatal("cleared templates are still in the state file")
	}
}

func TestIntegrationAPIRejectsInvalidMessages(t *testing.T) {
	app := newTestApp(t, true)
	input := templateIntegration()
	input.ID = ""
	input.ServerIDs = []string{"scuffedtards"}
	input.Messages = map[EventKind]MessageTemplate{RestartFailed: templateFixture("ok", "Hello {{nope}}", 1)}
	res := saveIntegration(t, app, "POST", "/api/integrations", input)
	if res.Code != 400 {
		t.Fatal(res.Code, res.Body.String())
	}
	body := decodeBody[struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}](t, res.Body.String())
	if body.Fields["messages.restart_failed.description"] == "" || !strings.Contains(body.Error, "{{nope}}") || !strings.Contains(body.Error, "restart_failed") {
		t.Fatalf("body = %+v", body)
	}
	if len(app.store.Snapshot().Integrations) != 0 {
		t.Fatal("invalid integration was stored")
	}
	input.Messages = nil
	input.ServerMessages = map[string]map[EventKind]MessageTemplate{"other": {ServerDown: templateFixture("a", "b", 1)}}
	if res := saveIntegration(t, app, "POST", "/api/integrations", input); res.Code != 400 || !strings.Contains(res.Body.String(), `"serverMessages.other"`) {
		t.Fatal(res.Code, res.Body.String())
	}
	if res := requestJSON(t, app, "POST", "/api/integrations", `{"messages":{"server_down":{"title":"a","description":"b","color":1,"extra":true}}}`); res.Code != 400 {
		t.Fatal("unknown template field accepted", res.Code)
	}
	// Removing a server while it still has an override is rejected rather than silently dropped.
	input.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {ServerDown: templateFixture("a", "b", 1)}}
	created := decodeBody[DiscordIntegration](t, saveIntegration(t, app, "POST", "/api/integrations", input).Body.String())
	input.ServerIDs = []string{}
	if res := saveIntegration(t, app, "PUT", "/api/integrations/"+created.ID, input); res.Code != 400 {
		t.Fatal("orphaned server override accepted", res.Code)
	}
}

func createTemplateIntegration(t *testing.T, app *App, edit func(*DiscordIntegration)) DiscordIntegration {
	t.Helper()
	input := templateIntegration()
	input.ID = ""
	input.ServerIDs = []string{"scuffedtards"}
	if edit != nil {
		edit(&input)
	}
	res := saveIntegration(t, app, "POST", "/api/integrations", input)
	if res.Code != 201 {
		t.Fatal(res.Code, res.Body.String())
	}
	return decodeBody[DiscordIntegration](t, res.Body.String())
}

func TestMessagePreview(t *testing.T) {
	app := newTestApp(t, true)
	saved := createTemplateIntegration(t, app, func(i *DiscordIntegration) {
		i.Messages = map[EventKind]MessageTemplate{RestartCompleted: templateFixture("Saved {{server}}", "saved body", 0x010203)}
		i.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {RestartCompleted: templateFixture("Server {{server}}", "server body", 0x040506)}}
	})
	before, _ := os.ReadFile(app.store.path)
	snapshot := app.store.Snapshot()
	path := "/api/integrations/" + saved.ID + "/preview"
	preview := func(body string) previewResponse {
		t.Helper()
		res := requestJSON(t, app, "POST", path, body)
		if res.Code != 200 {
			t.Fatal(res.Code, res.Body.String())
		}
		return decodeBody[previewResponse](t, res.Body.String())
	}

	got := preview(`{"kind":"restart_completed"}`)
	if got.Source != messageSourceIntegration || got.Embed.Title != "Saved Example server" || got.Embed.Color != 0x010203 || !got.Enabled || got.Embed.Footer.Text != "Dragonwilds C2" || got.Embed.Timestamp == "" || got.Embed.Fields[0].Value != "Example server" {
		t.Fatalf("saved preview = %+v", got)
	}
	got = preview(`{"kind":"restart_completed","serverId":"scuffedtards"}`)
	if got.Source != messageSourceServer || got.Embed.Title != "Server ScuffedTards" || got.SampleEvent.ServerID != "scuffedtards" || !strings.Contains(got.SampleEvent.Image, "rsdragonwilds-server:0.1.1") {
		t.Fatalf("server preview = %+v", got)
	}
	got = preview(`{"kind":"restart_completed","serverId":"scuffedtards","serverMessages":{}}`)
	if got.Source != messageSourceIntegration || got.Embed.Title != "Saved ScuffedTards" {
		t.Fatalf("cleared server override preview = %+v", got)
	}
	got = preview(`{"kind":"restart_completed","messages":{"restart_completed":{"title":"Draft {{imageTag}}","description":"{{players}} / {{maxPlayers}}","color":16711680}}}`)
	if got.Source != messageSourceIntegration || got.Embed.Title != "Draft v1.2.3" || got.Embed.Description != "unavailable / unavailable" || got.Embed.Color != 0xFF0000 {
		t.Fatalf("draft preview = %+v", got)
	}
	got = preview(`{"kind":"restart_completed","messages":{}}`)
	if got.Source != messageSourceDefault || got.Embed.Title != "Restart completed" {
		t.Fatalf("reset preview = %+v", got)
	}
	got = preview(`{"kind":"restart_completed","messages":{"restart_completed":{"title":"Restart completed","description":"The corpse has staggered back online. It did the bare minimum and expects a fucking parade."}}}`)
	if got.Source != messageSourceDefault || got.Embed.Color != 0x22C55E {
		t.Fatalf("default-equal draft = %+v", got)
	}
	got = preview(`{"kind":"server_stopped","messages":{"server_stopped":{"title":"Off","description":"x","color":1,"enabled":false}}}`)
	if got.Enabled || got.Embed.Title != "Off" {
		t.Fatalf("disabled preview = %+v", got)
	}
	got = preview(`{"kind":"player_joined"}`)
	if got.Embed.Title != "Player joined" || got.Embed.Description != "Brynn (Sam) joined the server." || got.Embed.Fields[1] != (discordEmbedField{Name: "Players", Value: "3/10"}) {
		t.Fatalf("player joined preview = %+v", got)
	}
	got = preview(`{"kind":"restart_warning"}`)
	if !strings.HasPrefix(got.Embed.Description, "Restart in 5 minutes.") {
		t.Fatalf("warning preview = %+v", got)
	}
	got = preview(`{"kind":"integration_test","messages":{"integration_test":{"title":"{{event}} {{server}}","description":"d"}}}`)
	if got.Embed.Title != "Integration test Unknown server" || len(got.Embed.Fields) != 0 {
		t.Fatalf("integration test preview = %+v", got)
	}

	for body, field := range map[string]string{
		`{"kind":"nope"}`: "kind", `{}`: "kind", `{"kind":"restart_completed","serverId":"other"}`: "serverId",
		`{"kind":"restart_completed","messages":{"restart_completed":{"title":"{{bad}}","description":"x"}}}`:            "messages.restart_completed.title",
		`{"kind":"restart_completed","messages":{"restart_completed":{"title":"a","description":"x","color":99999999}}}`: "messages.restart_completed.color",
		`{"kind":"restart_completed","serverMessages":{"other":{"restart_completed":{"title":"a","description":"x"}}}}`:  "serverMessages.other",
	} {
		res := requestJSON(t, app, "POST", path, body)
		if res.Code != 400 || decodeBody[struct{ Fields map[string]string }](t, res.Body.String()).Fields[field] == "" {
			t.Fatalf("%s: %d %s", body, res.Code, res.Body.String())
		}
	}
	if res := requestJSON(t, app, "POST", path, `{"kind":"restart_completed","bogus":1}`); res.Code != 400 {
		t.Fatal("unknown preview field accepted")
	}
	if res := requestJSON(t, app, "POST", "/api/integrations/missing/preview", `{"kind":"restart_completed"}`); res.Code != 404 {
		t.Fatal(res.Code)
	}
	if res := requestJSON(t, app, "GET", path, ""); res.Code != 405 {
		t.Fatal(res.Code)
	}
	after, _ := os.ReadFile(app.store.path)
	if string(before) != string(after) || !reflect.DeepEqual(snapshot, app.store.Snapshot()) {
		t.Fatal("preview changed state")
	}
}

func TestMessagePreviewRejectsWebhookTemplates(t *testing.T) {
	app := newTestApp(t, true)
	hook := integrationFixture()
	hook.ID, hook.Provider, hook.GuildID, hook.ChannelID, hook.WebhookURL = "", providerWebhook, "", "", "https://hooks.example.com/rsdw"
	created := decodeBody[DiscordIntegration](t, saveIntegration(t, app, "POST", "/api/integrations", hook).Body.String())
	path := "/api/integrations/" + created.ID
	if res := requestJSON(t, app, "POST", path+"/preview", `{"kind":"server_down","messages":{"server_down":{"title":"a","description":"b"}}}`); res.Code != 400 {
		t.Fatal(res.Code, res.Body.String())
	}
	if res := requestJSON(t, app, "POST", path+"/test", `{"kind":"server_down"}`); res.Code != 400 {
		t.Fatal("webhook test accepted a Discord-only kind", res.Code)
	}
	hook.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("a", "b", 1)}
	if res := saveIntegration(t, app, "PUT", path, hook); res.Code != 400 {
		t.Fatal("webhook templates accepted", res.Code)
	}
}

func TestIntegrationTestUsesSavedTemplatesAndKind(t *testing.T) {
	app := newTestApp(t, true)
	saved := createTemplateIntegration(t, app, func(i *DiscordIntegration) {
		i.Messages = map[EventKind]MessageTemplate{
			IntegrationTest:  templateFixture("Custom test", "from {{event}}", 0x0A0B0C),
			RestartCompleted: templateFixture("Integration {{server}}", "int body", 0x111111),
			ServerStopped:    {Title: "Never", Description: "off", Color: intPtr(1), Enabled: boolPtr(false)},
		}
		i.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {RestartCompleted: templateFixture("Server {{server}} {{imageTag}}", "srv body", 0x222222)}}
		i.Rules = map[EventKind]bool{}
	})
	path := "/api/integrations/" + saved.ID + "/test"
	send := func(body string) Delivery {
		t.Helper()
		res := requestJSON(t, app, "POST", path, body)
		if res.Code != 202 {
			t.Fatal(res.Code, res.Body.String())
		}
		return decodeBody[Delivery](t, res.Body.String())
	}
	embedOf := func(d Delivery) discordEmbed {
		t.Helper()
		stored := app.store.Snapshot().Deliveries[d.ID]
		message, err := buildDiscordMessage(stored)
		if err != nil {
			t.Fatal(err)
		}
		return message.Embeds[0]
	}

	d := send(``)
	if embed := embedOf(d); d.Event.Kind != IntegrationTest || !d.Test || embed.Title != "Custom test" || embed.Description != "from Integration test" || embed.Color != 0x0A0B0C || len(embed.Fields) != 0 {
		t.Fatalf("default test = %+v / %+v", d, embed)
	}
	d = send(`{}`)
	if d.Event.Kind != IntegrationTest {
		t.Fatal(d.Event.Kind)
	}
	d = send(`{"kind":"restart_completed"}`)
	if embed := embedOf(d); d.Template == nil || embed.Title != "Integration Example server" || embed.Fields[0].Value != "Example server" {
		t.Fatalf("integration-level kind test = %+v", embed)
	}
	d = send(`{"kind":"restart_completed","serverId":"scuffedtards"}`)
	if embed := embedOf(d); embed.Title != "Server ScuffedTards 0.1.1" || embed.Description != "srv body" || embed.Color != 0x222222 {
		t.Fatalf("server-level kind test = %+v", embed)
	}
	d = send(`{"kind":"server_down"}`)
	if embed := embedOf(d); d.Template != nil || embed.Title != "Server unhealthy" {
		t.Fatalf("default kind test = %+v / %+v", d.Template, embed)
	}
	// Disabled messages and unset rules do not block an explicit test.
	d = send(`{"kind":"server_stopped"}`)
	if embed := embedOf(d); embed.Title != "Never" {
		t.Fatalf("disabled kind test = %+v", embed)
	}
	for _, body := range []string{`{"kind":"nope"}`, `{"kind":"server_down","serverId":"other"}`, `{"bogus":1}`, `{`} {
		if res := requestJSON(t, app, "POST", path, body); res.Code != 400 {
			t.Fatalf("%s: %d", body, res.Code)
		}
	}
	if res := requestJSON(t, app, "POST", "/api/integrations/missing/test", `{}`); res.Code != 404 {
		t.Fatal(res.Code)
	}
	// Every kind, including the warning that needs a live schedule, is deliverable as a test.
	for _, spec := range discordMessageSpecs {
		send(`{"kind":"` + string(spec.Kind) + `"}`)
	}
	if err := app.processDeliveries(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, delivery := range app.store.Snapshot().Deliveries {
		if delivery.Status != DeliverySent {
			t.Fatalf("test delivery %s for %s is %s: %s", delivery.ID, delivery.Event.Kind, delivery.Status, delivery.Result)
		}
	}
}

func TestDisabledMessageSuppressesDelivery(t *testing.T) {
	app := newTestApp(t, true)
	saved := createTemplateIntegration(t, app, func(i *DiscordIntegration) {
		i.Rules = map[EventKind]bool{ServerDown: true, ServerRecovered: true}
		i.Messages = map[EventKind]MessageTemplate{ServerDown: {Title: "Down", Description: "d", Color: intPtr(1), Enabled: boolPtr(false)}}
		i.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {ServerRecovered: {Title: "Up", Description: "u", Color: intPtr(2), Enabled: boolPtr(false)}}}
	})
	emit := func() {
		if err := app.store.Update(func(s *State) error {
			emitAlert(s, s.Servers["scuffedtards"], ServerDown, "", "", time.Now())
			emitAlert(s, s.Servers["scuffedtards"], ServerRecovered, "", "", time.Now())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	countEvents := func() int {
		n := 0
		for _, event := range app.store.Snapshot().Events {
			if event.Kind == ServerDown || event.Kind == ServerRecovered {
				n++
			}
		}
		return n
	}
	eventsBefore := countEvents()
	emit()
	if got := len(app.store.Snapshot().Deliveries); got != 0 {
		t.Fatalf("disabled messages queued %d deliveries", got)
	}
	// Events still reach the event log.
	events := countEvents()
	if events-eventsBefore != 2 {
		t.Fatalf("events = %d", events)
	}
	// Re-enabling the server-level message queues new events only; disabling afterwards cancels pending ones.
	update := templateIntegration()
	update.ServerIDs, update.Rules, update.Name = []string{"scuffedtards"}, map[EventKind]bool{ServerDown: true, ServerRecovered: true}, saved.Name
	update.Messages = map[EventKind]MessageTemplate{ServerDown: {Title: "Down", Description: "d", Color: intPtr(1), Enabled: boolPtr(false)}}
	if res := saveEmptying(t, app, "/api/integrations/"+saved.ID, update, "serverMessages"); res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	emit()
	pending := app.store.Snapshot().Deliveries
	if len(pending) != 1 {
		t.Fatalf("deliveries = %d", len(pending))
	}
	update.Messages = map[EventKind]MessageTemplate{ServerDown: {Title: "Down", Description: "d", Color: intPtr(1), Enabled: boolPtr(false)}}
	update.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {ServerRecovered: {Title: "Up", Description: "u", Color: intPtr(2), Enabled: boolPtr(false)}}}
	if res := saveIntegration(t, app, "PUT", "/api/integrations/"+saved.ID, update); res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	for _, d := range app.store.Snapshot().Deliveries {
		if d.Status != DeliveryFailed || d.Result != "Cancelled by integration configuration change" {
			t.Fatalf("pending delivery survived disabling: %+v", d)
		}
	}
}

func TestDeliveryKeepsTemplateInEffectWhenCreated(t *testing.T) {
	app := newTestApp(t, true)
	saved := createTemplateIntegration(t, app, func(i *DiscordIntegration) {
		i.Rules = map[EventKind]bool{ServerDown: true, RestartCompleted: true}
		i.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("Before {{server}}", "old", 0xAAAAAA)}
	})
	var down, restarted Event
	if err := app.store.Update(func(s *State) error {
		down = emitAlert(s, s.Servers["scuffedtards"], ServerDown, "", "", time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	update := templateIntegration()
	update.Name, update.ServerIDs, update.Rules = saved.Name, []string{"scuffedtards"}, map[EventKind]bool{ServerDown: true, RestartCompleted: true}
	update.Messages = map[EventKind]MessageTemplate{ServerDown: templateFixture("After {{server}}", "new", 0xBBBBBB)}
	if res := saveIntegration(t, app, "PUT", "/api/integrations/"+saved.ID, update); res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	if err := app.store.Update(func(s *State) error {
		restarted = emitAlert(s, s.Servers["scuffedtards"], RestartCompleted, "", "", time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := app.store.Snapshot()
	views := discordDeliveryViews(deliveriesOf(snapshot))
	titles := map[EventKind]string{}
	for _, view := range views {
		titles[view.Event.Kind] = view.Embed.Title
	}
	if titles[ServerDown] != "Before ScuffedTards" || titles[RestartCompleted] != "Restart completed" {
		t.Fatalf("titles = %v", titles)
	}
	if down.ID == "" || restarted.ID == "" {
		t.Fatal("events were not emitted")
	}
	// The message sent for the pending delivery is the same one the history shows.
	for _, d := range snapshot.Deliveries {
		message, err := buildDiscordMessage(d)
		if err != nil {
			t.Fatal(err)
		}
		if d.Event.Kind == ServerDown && message.Embeds[0].Title != "Before ScuffedTards" {
			t.Fatalf("sent title = %q", message.Embeds[0].Title)
		}
	}
	// A delivery stored before templates existed has no snapshot and renders the built-in text.
	legacy := Delivery{ID: "old", Event: Event{Kind: ServerDown, ServerName: "S"}}
	if view := discordDeliveryViews([]Delivery{legacy})[0]; view.Embed.Title != "Server unhealthy" {
		t.Fatalf("legacy delivery = %+v", view.Embed)
	}
	// The snapshot is independent of the snapshot returned to callers.
	d := snapshot.Deliveries[queueDeliveryID(snapshot, ServerDown)]
	*d.Template.Color = 0
	if got := *app.store.Snapshot().Deliveries[d.ID].Template.Color; got != 0xAAAAAA {
		t.Fatalf("template shared with snapshot: %x", got)
	}
}

func deliveriesOf(s State) []Delivery {
	var out []Delivery
	for _, d := range s.Deliveries {
		out = append(out, d)
	}
	return out
}

func queueDeliveryID(s State, kind EventKind) string {
	for id, d := range s.Deliveries {
		if d.Event.Kind == kind {
			return id
		}
	}
	return ""
}

func TestEventCarriesServerImage(t *testing.T) {
	s := State{Integrations: map[string]DiscordIntegration{}, Deliveries: map[string]Delivery{}}
	server := Server{ID: "w", Name: "W", CurrentImage: "ghcr.io/x/server:1.0", DesiredImage: "ghcr.io/x/server:2.0"}
	if event := emitAlert(&s, server, ServerDown, "", "", time.Now()); event.Image != "ghcr.io/x/server:1.0" {
		t.Fatalf("image = %q", event.Image)
	}
	server.CurrentImage = ""
	if event := emitAlert(&s, server, ServerDown, "", "", time.Now()); event.Image != "ghcr.io/x/server:2.0" {
		t.Fatalf("image = %q", event.Image)
	}
}

func TestMessagesPersistAndLegacyStateLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"servers":{"scuffedtards":{"id":"scuffedtards","name":"ScuffedTards"}},"events":[],"users":{},"integrations":{"bot":{"id":"bot","name":"Old bot","enabled":true,"guildId":"1","channelId":"2","secretRef":{"name":"discord-bot","key":"token"},"serverIds":["scuffedtards"],"rules":{"server_down":true}}},"deliveries":{"d":{"id":"d","integrationId":"bot","event":{"id":"e","kind":"server_down","serverName":"ScuffedTards"},"guildId":"1","channelId":"2","status":"pending","attempts":0}}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: store, orchestrator: demoOrchestrator{}, auth: &Auth{demo: true}}
	old := store.Snapshot().Integrations["bot"]
	if old.Messages != nil || old.ServerMessages != nil || old.resolveMessage("scuffedtards", ServerDown).Source != messageSourceDefault {
		t.Fatalf("legacy integration = %+v", old)
	}
	listed := decodeBody[struct {
		Integrations []integrationView `json:"integrations"`
		Deliveries   []struct {
			Embed *discordEmbed `json:"embed"`
		} `json:"deliveries"`
	}](t, requestJSON(t, app, "GET", "/api/integrations", "").Body.String())
	if got := listed.Integrations[0].EffectiveMessages[ServerDown]; got.Source != messageSourceDefault || got.Title != "Server unhealthy" {
		t.Fatalf("effective = %+v", got)
	}
	if listed.Deliveries[0].Embed == nil || listed.Deliveries[0].Embed.Title != "Server unhealthy" {
		t.Fatalf("legacy delivery view = %+v", listed.Deliveries)
	}

	update := integrationFixture()
	update.Name = "Old bot"
	update.Rules = map[EventKind]bool{ServerDown: true}
	update.Messages = map[EventKind]MessageTemplate{ServerDown: {Title: "Persisted {{server}}", Description: "yes", Color: intPtr(0x445566)}, RestartFailed: {Title: "x", Description: "y", Color: intPtr(1), Enabled: boolPtr(false)}}
	update.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {ServerDown: templateFixture("Server override", "z", 9)}}
	if res := saveIntegration(t, app, "PUT", "/api/integrations/bot", update); res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	reloaded, err := NewStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Snapshot().Integrations["bot"]
	want := store.Snapshot().Integrations["bot"]
	if !reflect.DeepEqual(got, want) || got.Messages[ServerDown].Title != "Persisted {{server}}" || *got.ServerMessages["scuffedtards"][ServerDown].Color != 9 || got.Messages[RestartFailed].enabled() {
		t.Fatalf("round trip\n got  %+v\n want %+v", got, want)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"messages"`) || !strings.Contains(string(raw), `"serverMessages"`) {
		t.Fatal("templates are not in the state file")
	}
}
