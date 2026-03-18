package setup

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	colorGreen = "\033[32m"
	colorRed   = "\033[31m"
	colorCyan  = "\033[36m"
	colorReset = "\033[0m"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type taskUI struct {
	out        io.Writer
	isTerminal bool
}

func newTaskUI(out io.Writer) taskUI {
	return taskUI{
		out:        out,
		isTerminal: writerIsTerminal(out),
	}
}

func (ui taskUI) Run(message string, task func() (string, error)) error {
	if !ui.isTerminal {
		fmt.Fprintf(ui.out, "%s...\n", message)
		logs, err := task()
		return ui.finish(message, logs, err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go ui.spin(message, stop, done)

	logs, err := task()
	close(stop)
	<-done

	fmt.Fprint(ui.out, "\r\033[K")
	return ui.finish(message, logs, err)
}

func (ui taskUI) finish(message, logs string, err error) error {
	if err != nil {
		fmt.Fprintf(ui.out, "%s✗%s %s\n", colorRed, colorReset, message)
		if logs = strings.TrimSpace(logs); logs != "" {
			fmt.Fprintln(ui.out, logs)
		}
		return err
	}

	fmt.Fprintf(ui.out, "%s✓%s %s\n", colorGreen, colorReset, message)
	return nil
}

func (ui taskUI) spin(message string, stop <-chan struct{}, done chan<- struct{}) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	defer close(done)

	index := 0
	for {
		frame := spinnerFrames[index%len(spinnerFrames)]
		fmt.Fprintf(ui.out, "\r%s%s%s %s", colorCyan, frame, colorReset, message)

		select {
		case <-stop:
			return
		case <-ticker.C:
			index++
		}
	}
}

func writerIsTerminal(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}

	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}
