package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

const zwsp = "​"

func TestEscapeDiscordText(t *testing.T) {
	cases := map[string]string{
		"plain":                        "plain",
		"@everyone":                    "@" + zwsp + "everyone",
		"@here":                        "@" + zwsp + "here",
		"<@123>":                       "<" + zwsp + "@" + zwsp + "123\\>",
		"<@&456>":                      "<" + zwsp + "@" + zwsp + "&456\\>",
		"<#789>":                       "<" + zwsp + "\\#789\\>",
		"*b* _i_ ~s~ |sp| `c`":         "\\*b\\* \\_i\\_ \\~s\\~ \\|sp\\| \\`c\\`",
		"> quote # head - item":        "\\> quote \\# head \\- item",
		"[text](https://evil.example)": "\\[text\\]\\(https://evil.example\\)",
		`back\slash`:                   `back\\slash`,
		"":                             "",
	}
	for in, want := range cases {
		if got := escapeDiscordText(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostileValuesAreEscapedInEmbeds(t *testing.T) {
	event := Event{
		Kind: PlayerJoined, ServerName: "@everyone [Play here](https://evil.example)", Timestamp: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Image: "evil.example/<#9>_x:t-1",
		Evidence: AlertEvidence{
			PlayerCount:   &PlayerCountEvidence{Available: true, Players: 2, MaxPlayers: 10},
			JoinedPlayers: []ConnectedPlayer{{Name: "<@123>", CharacterName: "**Boss** @here"}, {Name: "_x_", CharacterName: "[a](b)"}},
		},
	}
	hostile := []string{"@everyone", "@here", "<@123>", "<@&9>", "](https://evil.example)", "**Boss**", "[a](b)"}
	assertSafe := func(label string, embed discordEmbed) {
		t.Helper()
		all := embed.Title + "\n" + embed.Description
		for _, field := range embed.Fields {
			all += "\n" + field.Name + "\n" + field.Value
		}
		for _, raw := range hostile {
			if strings.Contains(all, raw) {
				t.Errorf("%s: live %q in %q", label, raw, all)
			}
		}
	}

	custom := templateFixture("{{server}} / {{character}}", "{{player}} | {{joinedPlayers}} | {{image}} | {{imageTag}} | {{server}}", 0x123456)
	embed, err := renderDiscordEmbed(event, &custom)
	if err != nil {
		t.Fatal(err)
	}
	assertSafe("custom", embed)
	if want := "@" + zwsp + "everyone \\[Play here\\]\\(https://evil.example\\) / \\*\\*Boss\\*\\* @" + zwsp + "here, \\[a\\]\\(b\\)"; embed.Title != want {
		t.Fatalf("title = %q, want %q", embed.Title, want)
	}
	wantDescription := "<" + zwsp + "@" + zwsp + "123\\>, \\_x\\_ | " +
		"\\*\\*Boss\\*\\* @" + zwsp + "here (<" + zwsp + "@" + zwsp + "123\\>), \\[a\\]\\(b\\) (\\_x\\_) | " +
		"evil.example/<" + zwsp + "\\#9\\>\\_x:t\\-1 | t\\-1 | " +
		"@" + zwsp + "everyone \\[Play here\\]\\(https://evil.example\\)"
	if embed.Description != wantDescription {
		t.Fatalf("description = %q\nwant        %q", embed.Description, wantDescription)
	}
	if embed.Fields[0] != (discordEmbedField{Name: "Server", Value: "@" + zwsp + "everyone \\[Play here\\]\\(https://evil.example\\)"}) {
		t.Fatalf("server field = %+v", embed.Fields[0])
	}
	if embed.Fields[1].Value != "2/10" || strings.Contains(embed.Timestamp, "\\") {
		t.Fatalf("trusted values were escaped: %+v %q", embed.Fields, embed.Timestamp)
	}

	// The built-in player-joined roster text goes through the same escaping.
	embed, err = renderDiscordEmbed(event, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertSafe("default roster", embed)
	if want := "\\*\\*Boss\\*\\* @" + zwsp + "here (<" + zwsp + "@" + zwsp + "123\\>), \\[a\\]\\(b\\) (\\_x\\_) joined the server."; embed.Description != want {
		t.Fatalf("roster = %q, want %q", embed.Description, want)
	}

	message, err := buildDiscordMessage(Delivery{ID: "d", Event: event})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(message)
	if !strings.Contains(string(data), `"allowed_mentions":{"parse":[]}`) {
		t.Fatalf("mentions are not disabled: %s", data)
	}
}

func TestEscapedValuesStillFitEmbedLimits(t *testing.T) {
	// Escaping doubles every markdown character, so a long hostile name must still be truncated to Discord's
	// limits, including the 6000 character total.
	event := Event{Kind: ServerDown, ServerName: strings.Repeat("*", 5000)}
	tmpl := templateFixture("{{server}}", strings.Repeat("{{server}}", 4), 1)
	embed, err := renderDiscordEmbed(event, &tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if total := embedTotalLength(embed); total > discordEmbedTotalLimit {
		t.Fatalf("total = %d", total)
	}
	if n := len([]rune(embed.Title)); n != discordTitleLimit {
		t.Fatalf("title runes = %d", n)
	}
}

func TestColorOnlyCustomizationKeepsEvidenceVariant(t *testing.T) {
	joined := Event{Kind: PlayerJoined, ServerName: "World", Evidence: AlertEvidence{JoinedPlayers: []ConnectedPlayer{{Name: "Sam", CharacterName: "Brynn"}}}}
	warning := Event{Kind: RestartWarning, ServerName: "World", Evidence: AlertEvidence{RestartWarning: &RestartWarningEvidence{RestartAt: time.Date(2026, 1, 1, 13, 5, 0, 0, time.UTC), Minutes: 5}}}
	for _, tc := range []struct {
		event Event
		want  string
	}{
		{joined, "Brynn (Sam) joined the server."},
		{warning, "Restart in 5 minutes. Save your progress. Scheduled for 2026-01-01T13:05:00Z."},
	} {
		def := defaultMessageTemplate(tc.event.Kind)
		recolored := MessageTemplate{Title: def.Title, Description: def.Description, Color: intPtr(0xABCDEF)}
		embed, err := renderDiscordEmbed(tc.event, &recolored)
		if err != nil {
			t.Fatal(err)
		}
		if embed.Description != tc.want || embed.Color != 0xABCDEF {
			t.Errorf("%s: color-only template = %q / %x", tc.event.Kind, embed.Description, embed.Color)
		}
		changed := MessageTemplate{Title: def.Title, Description: "Custom text", Color: intPtr(0xABCDEF)}
		if embed, _ = renderDiscordEmbed(tc.event, &changed); embed.Description != "Custom text" {
			t.Errorf("%s: customised text lost: %q", tc.event.Kind, embed.Description)
		}
		retitled := MessageTemplate{Title: "Custom title", Description: def.Description, Color: intPtr(0xABCDEF)}
		if embed, _ = renderDiscordEmbed(tc.event, &retitled); embed.Title != "Custom title" || embed.Description != def.Description {
			t.Errorf("%s: customised title kept the variant: %+v", tc.event.Kind, embed)
		}
	}

	// Through the API: saving the default text with only a new color keeps the evidence variant for new
	// deliveries, and enabled=false still silences the kind.
	app := newTestApp(t, true)
	saved := createTemplateIntegration(t, app, func(i *DiscordIntegration) {
		def := defaultMessageTemplate(PlayerJoined)
		i.Messages = map[EventKind]MessageTemplate{PlayerJoined: {Title: def.Title, Description: def.Description, Color: intPtr(0x010203)}}
	})
	stored := saved.Messages[PlayerJoined]
	if stored.Color == nil || *stored.Color != 0x010203 || stored.Title != "Player count increased" {
		t.Fatalf("stored = %+v", saved.Messages)
	}
	var delivery Delivery
	if err := app.store.Update(func(state *State) error {
		delivery = queueDeliveryForNewEvent(state, state.Integrations[saved.ID], joined)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	message, err := buildDiscordMessage(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if embed := message.Embeds[0]; embed.Color != 0x010203 || embed.Description != "Brynn (Sam) joined the server." {
		t.Fatalf("delivery embed = %+v", embed)
	}
	preview := requestJSON(t, app, "POST", "/api/integrations/"+saved.ID+"/preview", `{"kind":"player_joined"}`)
	if got := decodeBody[previewResponse](t, preview.Body.String()); got.Source != messageSourceIntegration || got.Embed.Description != "Brynn (Sam) joined the server." || got.Embed.Color != 0x010203 {
		t.Fatalf("preview = %+v", got)
	}

	disabled := defaultMessageTemplate(RestartWarning)
	disabled.Enabled = boolPtr(false)
	i := templateIntegration()
	i.Messages = map[EventKind]MessageTemplate{RestartWarning: disabled}
	i.normalizeMessages()
	if i.messageEnabled("scuffedtards", RestartWarning) {
		t.Fatal("enabled=false was dropped")
	}
	if embed, _ := renderDiscordEmbed(warning, &disabled); !strings.HasPrefix(embed.Description, "Restart in 5 minutes.") {
		t.Fatalf("disabled-only template lost the variant: %q", embed.Description)
	}
}

func TestLegacyPutCanUntickServerWithOverride(t *testing.T) {
	app := newTestApp(t, true)
	saved := createTemplateIntegration(t, app, func(i *DiscordIntegration) {
		i.Messages = map[EventKind]MessageTemplate{RestartCompleted: templateFixture("Integration", "body", 0x111111)}
		i.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {RestartCompleted: templateFixture("Server", "srv", 0x222222)}}
	})
	if len(saved.ServerMessages["scuffedtards"]) != 1 {
		t.Fatalf("setup: %+v", saved.ServerMessages)
	}

	// The legacy console form sends no template keys at all.
	legacy := func(serverIDs string) string {
		return `{"name":"Discord alerts","enabled":true,"guildId":"123","channelId":"456","secretRef":{"name":"discord-bot","key":"token"},"serverIds":` + serverIDs + `,"rules":{"restart_completed":true}}`
	}
	path := "/api/integrations/" + saved.ID

	// Keeping the server keeps its override.
	if res := requestJSON(t, app, "PUT", path, legacy(`["scuffedtards"]`)); res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	if got := app.store.Snapshot().Integrations[saved.ID]; len(got.ServerMessages["scuffedtards"]) != 1 {
		t.Fatalf("override lost while the server stays: %+v", got.ServerMessages)
	}

	// Unticking it drops the override with the association and keeps the integration templates.
	res := requestJSON(t, app, "PUT", path, legacy(`[]`))
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	got := app.store.Snapshot().Integrations[saved.ID]
	if got.ServerMessages != nil || len(got.ServerIDs) != 0 || got.Messages[RestartCompleted].Title != "Integration" {
		t.Fatalf("stored = %+v", got)
	}
	if body := decodeBody[DiscordIntegration](t, res.Body.String()); body.ServerMessages != nil {
		t.Fatalf("response = %+v", body)
	}

	// An override the request itself sends for an unassociated server is still a 400.
	explicit := strings.TrimSuffix(legacy(`[]`), "}") + `,"serverMessages":{"scuffedtards":{"restart_completed":{"title":"a","description":"b"}}}}`
	res = requestJSON(t, app, "PUT", path, explicit)
	if res.Code != 400 || decodeBody[struct{ Fields map[string]string }](t, res.Body.String()).Fields["serverMessages.scuffedtards"] == "" {
		t.Fatalf("explicit override for a removed server: %d %s", res.Code, res.Body.String())
	}
}

func TestMessageValidationEdgeCases(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	cases := []struct {
		name  string
		tmpl  MessageTemplate
		field string
		text  string
	}{
		{"whitespace-only title with description", templateFixture("   \t", "body", 1), "title", "whitespace"},
		{"whitespace-only description with title", templateFixture("title", "  \n ", 1), "description", "whitespace"},
		{"NUL in title", templateFixture("a\x00b", "body", 1), "title", "NUL"},
		{"NUL in description", templateFixture("title", "a\x00b", 1), "description", "NUL"},
		{"padded title over the limit", templateFixture(long(257), "body", 1), "title", "257"},
	}
	for _, tc := range cases {
		err := validateMessageTemplate("messages.server_down", ServerDown, tc.tmpl)
		if err == nil || integrationErrorFields(err)["messages.server_down."+tc.field] == "" || !strings.Contains(err.Error(), tc.text) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// Whitespace around real text is allowed, counted trimmed, and stored trimmed.
	padded := templateFixture("  "+long(256)+" ", "\n  body \n", 1)
	if err := validateMessageTemplate("messages.server_down", ServerDown, padded); err != nil {
		t.Fatal(err)
	}
	i := templateIntegration()
	i.Messages = map[EventKind]MessageTemplate{ServerDown: padded}
	i.normalizeMessages()
	if got := i.Messages[ServerDown]; got.Title != long(256) || got.Description != "body" {
		t.Fatalf("stored = %q / %q", got.Title, got.Description)
	}
	// Every part is limited on its own (256 + 4096), so the 6000 character embed total cannot be reached by a
	// valid template; the render-side cap is covered by TestEscapedValuesStillFitEmbedLimits.
	if err := validateMessageTemplate("messages.server_down", ServerDown, templateFixture(long(256), long(4096), 1)); err != nil {
		t.Fatal(err)
	}
	if err := validateMessageTemplate("messages.server_down", ServerDown, templateFixture(long(256), long(4097), 1)); err == nil {
		t.Fatal("over-long description accepted")
	}
}

func TestServerOverrideEqualToIntegrationTemplateIsNormalisedAway(t *testing.T) {
	i := templateIntegration()
	same := templateFixture("Integration", "body", 0x111111)
	i.Messages = map[EventKind]MessageTemplate{RestartCompleted: same}
	i.ServerMessages = map[string]map[EventKind]MessageTemplate{"scuffedtards": {RestartCompleted: same, ServerDown: templateFixture("Down", "x", 2)}}
	i.normalizeMessages()
	if _, ok := i.ServerMessages["scuffedtards"][RestartCompleted]; ok || len(i.ServerMessages["scuffedtards"]) != 1 {
		t.Fatalf("server messages = %+v", i.ServerMessages)
	}
}

func TestOptionalBodyRejectsTrailingData(t *testing.T) {
	app := newTestApp(t, true)
	saved := createTemplateIntegration(t, app, nil)
	for _, route := range []string{"/test", "/preview"} {
		for _, body := range []string{`{"kind":"restart_completed"} {}`, `{"kind":"restart_completed"}x`, `{"kind":"restart_completed"}{"kind":"server_down"}`} {
			before := app.store.Snapshot()
			res := requestJSON(t, app, "POST", "/api/integrations/"+saved.ID+route, body)
			if res.Code != 400 {
				t.Errorf("%s %q = %d %s", route, body, res.Code, res.Body.String())
			}
			if !reflect.DeepEqual(before.Deliveries, app.store.Snapshot().Deliveries) {
				t.Errorf("%s %q queued a delivery", route, body)
			}
		}
	}
	for _, body := range []string{``, `{}`, `{"kind":"restart_completed"}  `} {
		kind := `{"kind":"restart_completed"}`
		if body == `` || body == `{}` {
			kind = body
		}
		if res := requestJSON(t, app, "POST", "/api/integrations/"+saved.ID+"/test", body); res.Code != 202 {
			t.Errorf("test %q (%s) = %d %s", body, kind, res.Code, res.Body.String())
		}
	}
}

func TestPreviewAndTestRequireAdminAndCSRF(t *testing.T) {
	app, issuer := oidcTestApp(t)
	viewer, viewerCSRF := loginAs(t, app, issuer, "viewer")
	admin, csrf := loginAs(t, app, issuer, "admin")
	input := integrationFixture()
	input.ID = ""
	create := authRequest(app, "POST", "/api/integrations", admin, app.auth.settings.Origin, csrf, integrationJSON(t, input))
	if create.Code != 201 {
		t.Fatal(create.Code, create.Body.String())
	}
	id := decodeBody[DiscordIntegration](t, create.Body.String()).ID
	body := `{"kind":"restart_completed","messages":{"restart_completed":{"title":"t","description":"d"}}}`
	for _, route := range []string{"/api/integrations/" + id + "/preview", "/api/integrations/" + id + "/test"} {
		before := app.store.Snapshot()
		if res := authRequest(app, "POST", route, viewer, app.auth.settings.Origin, viewerCSRF, body); res.Code != 403 {
			t.Errorf("viewer %s = %d %s", route, res.Code, res.Body.String())
		}
		if res := authRequest(app, "POST", route, nil, "", "", body); res.Code != 401 {
			t.Errorf("anonymous %s = %d", route, res.Code)
		}
		for _, pair := range [][2]string{{app.auth.settings.Origin, ""}, {app.auth.settings.Origin, "wrong"}, {"", csrf}, {"https://evil.example", csrf}} {
			if res := authRequest(app, "POST", route, admin, pair[0], pair[1], body); res.Code != 403 {
				t.Errorf("admin %s origin %q csrf %q = %d", route, pair[0], pair[1], res.Code)
			}
		}
		if !reflect.DeepEqual(before, app.store.Snapshot()) {
			t.Errorf("%s: a denied request changed state", route)
		}
	}
	if res := authRequest(app, "POST", "/api/integrations/"+id+"/preview", admin, app.auth.settings.Origin, csrf, body); res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	if res := authRequest(app, "POST", "/api/integrations/"+id+"/test", admin, app.auth.settings.Origin, csrf, `{"kind":"restart_completed"}`); res.Code != 202 {
		t.Fatal(res.Code, res.Body.String())
	}
}
