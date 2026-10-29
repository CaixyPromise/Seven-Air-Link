package app

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestGlobalFlagsDoNotConsumeMessageText(t *testing.T) {
	args, opt, e := common([]string{"message", "send", "--text", "--config", "--to", "shared", "--config", "bot.json", "--output", "json"})
	if e != nil || opt.config != "bot.json" || !reflect.DeepEqual(args, []string{"message", "send", "--text", "--config", "--to", "shared"}) {
		t.Fatal(args, opt, e)
	}
	args, opt, e = common([]string{"--config", "bot.json", "message", "send", "--text=--output", "--output=json"})
	if e != nil || opt.output != "json" || args[len(args)-1] != "--text=--output" {
		t.Fatal(args, opt, e)
	}
}
func TestEmbeddedSkillAndCLIValidation(t *testing.T) {
	var out, err bytes.Buffer
	if code := Run(context.Background(), []string{"skill", "show", "--output", "json"}, bytes.NewReader(nil), &out, &err); code != 0 {
		t.Fatal(out.String(), err.String())
	}
	var got map[string]string
	if e := json.Unmarshal(out.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains([]byte(got["content"]), []byte("--incoming-only")) {
		t.Fatal("stale Skill")
	}
	out.Reset()
	if Run(context.Background(), []string{"message", "watch", "--timeout", "-1s", "--output", "json"}, bytes.NewReader(nil), &out, &err) == 0 {
		t.Fatal("negative watch duration accepted")
	}
	if !json.Valid(out.Bytes()) {
		t.Fatal("non-machine-readable failure", out.String())
	}
}
