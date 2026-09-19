package migrate

import "testing"

func TestRewriteMastodonSettingsPreservesStoredJSON(t *testing.T) {
	source := `{"z":9007199254740993,"theme":"contrast","nested": {"z":2,"a":[9007199254740995,"<keep>&"]},"a":1}`
	got, err := rewriteMastodonSettings(source, []mastodonSettingUpdate{
		{"web.color_scheme", "dark"}, {"web.contrast", "high"}, {"theme", "default"}, {"added", "<new>&"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"z":9007199254740993,"theme":"default","nested":{"z":2,"a":[9007199254740995,"<keep>&"]},"a":1,"web.color_scheme":"dark","web.contrast":"high","added":"<new>&"}`
	if string(got) != want {
		t.Fatalf("stored JSON = %s, want %s", got, want)
	}
}

func TestRewriteMastodonSettingsMatchesHashReplacementOrder(t *testing.T) {
	got, err := rewriteMastodonSettings(`{"second":2,"first":1,"second":3}`, []mastodonSettingUpdate{{"first", false}, {"new", true}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"second":3,"first":false,"new":true}` {
		t.Fatalf("stored JSON = %s", got)
	}
	for _, invalid := range []string{`null`, `[]`, `{"a":1} {}`, `{"a":`} {
		if _, err := rewriteMastodonSettings(invalid, nil); err == nil {
			t.Errorf("accepted invalid settings %q", invalid)
		}
	}
}
