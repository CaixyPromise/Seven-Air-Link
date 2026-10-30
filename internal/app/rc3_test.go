package app

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestRC3FlagValuesAreNeverIdentitySwitches(t *testing.T) {
	input := []string{"note", "set", "--text", "--config", "--revision", "2", "--config", "bot.json", "--output", "json"}
	rest, opt, err := common(input)
	if err != nil || !opt.configExplicit || opt.config != "bot.json" || !reflect.DeepEqual(rest, []string{"note", "set", "--text", "--config", "--revision", "2"}) {
		t.Fatalf("flag parse: %v %#v %v", rest, opt, err)
	}
}
func TestRC3UnsafeCLIFormsAreRejected(t *testing.T) {
	for _, args := range [][]string{{"auth", "password", "set", "--password", "do-not-accept-secrets-on-argv"}, {"auth", "password", "set", "--config", "owner.json", "--data", "server"}, {"storage", "restore", "--from", "backup", "--out", "wrong-destination"}} {
		var stdout, stderr bytes.Buffer
		args = append(args, "--output", "json")
		code := Run(context.Background(), args, bytes.NewReader(nil), &stdout, &stderr)
		var v map[string]any
		if code == 0 || json.Unmarshal(stdout.Bytes(), &v) != nil || v["error"] == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
}
