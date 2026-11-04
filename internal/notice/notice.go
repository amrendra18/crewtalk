package notice

import (
	"context"
	"os"
	"os/exec"
	"time"
)

var wait = 2 * time.Second

func Show(title, body string) {
	if os.Getenv("HERDR_ENV") != "1" {
		return
	}
	path, err := exec.LookPath("herdr")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	exec.CommandContext(ctx, path, "notification", "show", title, "--body", body).Run()
}
