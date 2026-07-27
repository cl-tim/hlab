package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ErrPromptAborted is returned when the operator asks to stop.
var ErrPromptAborted = errors.New("aborted")

// prompter asks the operator questions. Input and output are injected so the
// interactive flows can be driven by tests.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	// fd is the terminal file descriptor used to disable echo for password
	// entry. It is -1 when input is not a terminal.
	fd int
}

// newPrompter builds a prompter over the given streams. Password echo can only
// be suppressed when reading from a real terminal, so when it is not, password
// prompts refuse to read rather than echoing the secret.
func newPrompter(in io.Reader, out io.Writer) *prompter {
	p := &prompter{in: bufio.NewReader(in), out: out, fd: -1}

	if f, ok := in.(*os.File); ok {
		fd := int(f.Fd())
		if term.IsTerminal(fd) {
			p.fd = fd
		}
	}
	return p
}

// interactive reports whether input is a terminal.
func (p *prompter) interactive() bool { return p.fd >= 0 }

// readLine reads one line, mapping EOF to an abort.
func (p *prompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
			return "", ErrPromptAborted
		}
		if !errors.Is(err, io.EOF) {
			return "", err
		}
	}
	return strings.TrimSpace(line), nil
}

// Confirm asks a yes/no question. An empty answer takes the default.
func (p *prompter) Confirm(question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Fprintf(p.out, "%s [%s] ", question, hint)

	answer, err := p.readLine()
	if err != nil {
		return false, err
	}

	switch strings.ToLower(answer) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, nil
	}
}

// Choice asks a question answered by one of the given single letters, and
// returns the chosen letter in lower case.
func (p *prompter) Choice(question string, options []string, def string) (string, error) {
	fmt.Fprintf(p.out, "%s [%s] ", question, strings.Join(options, "/"))

	answer, err := p.readLine()
	if err != nil {
		return "", err
	}
	answer = strings.ToLower(answer)
	if answer == "" {
		return def, nil
	}
	for _, opt := range options {
		if strings.EqualFold(answer, opt) {
			return strings.ToLower(opt), nil
		}
	}
	return def, nil
}

// Line asks for a value, offering a default.
func (p *prompter) Line(question, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s] ", question, def)
	} else {
		fmt.Fprintf(p.out, "%s ", question)
	}

	answer, err := p.readLine()
	if err != nil {
		return "", err
	}
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

// Password reads a secret without echoing it.
//
// If input is not a terminal the secret cannot be hidden, so rather than
// echoing a password into a log or a transcript this refuses. Enrolment then
// falls back to key-based authentication.
func (p *prompter) Password(question string) (string, error) {
	if !p.interactive() {
		return "", errors.New("cannot read a password without a terminal")
	}

	fmt.Fprintf(p.out, "%s ", question)
	secret, err := term.ReadPassword(p.fd)
	fmt.Fprintln(p.out)
	if err != nil {
		return "", err
	}
	return string(secret), nil
}

// askYesNo is the simple confirmation used by non-interactive commands.
func askYesNo(r io.Reader, w io.Writer, question string) (bool, error) {
	ok, err := newPrompter(r, w).Confirm(question, false)
	if errors.Is(err, ErrPromptAborted) {
		fmt.Fprintln(w)
		return false, nil
	}
	return ok, err
}
