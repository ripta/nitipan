package wire

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// SSE is one server-sent event as the parser reads it.
type SSE struct {
	ID    string
	Event string
	Data  string
}

// WriteSSE writes an event. A newline in the data becomes another data line, so a payload cannot
// end its event early.
func WriteSSE(w io.Writer, ev SSE) error {
	var b strings.Builder

	if ev.ID != "" {
		fmt.Fprintf(&b, "id: %s\n", oneLine(ev.ID))
	}

	if ev.Event != "" {
		fmt.Fprintf(&b, "event: %s\n", oneLine(ev.Event))
	}

	for _, line := range strings.Split(ev.Data, "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}

	b.WriteString("\n")

	_, err := io.WriteString(w, b.String())
	if err != nil {
		return fmt.Errorf("writing an event: %w", err)
	}

	return nil
}

// WriteComment writes a comment line, which a client ignores. The stream sends one periodically so
// a proxy does not drop it as idle.
func WriteComment(w io.Writer, text string) error {
	_, err := fmt.Fprintf(w, ": %s\n\n", oneLine(text))
	if err != nil {
		return fmt.Errorf("writing a comment: %w", err)
	}

	return nil
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// maxLine bounds a single line, so a broker that streams without newlines cannot grow the buffer.
const maxLine = 1 << 20

// Parser reads server-sent events from a stream. Go ships no client for them.
type Parser struct {
	r *bufio.Reader

	// OnLine, when set, sees every line read, comments included. A caller uses it to tell that the
	// stream is alive.
	OnLine func()
}

// NewParser reads events from r.
func NewParser(r io.Reader) *Parser {
	return &Parser{r: bufio.NewReaderSize(r, 4096)}
}

// Next returns the next event that carries data. Comments, retry hints, and fields it does not know
// are skipped. It returns io.EOF when the stream ends between events. A stream that ends inside an
// event drops that event, as the specification requires.
func (p *Parser) Next() (SSE, error) {
	var (
		ev       SSE
		data     []string
		hasField bool
	)

	for {
		line, err := p.readLine()
		if err != nil {
			return SSE{}, err
		}

		if p.OnLine != nil {
			p.OnLine()
		}

		if line == "" {
			if hasField && len(data) > 0 {
				ev.Data = strings.Join(data, "\n")

				return ev, nil
			}

			ev, data, hasField = SSE{}, nil, false

			continue
		}

		if strings.HasPrefix(line, ":") {
			continue
		}

		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")

		switch name {
		case "id":
			ev.ID = value
			hasField = true
		case "event":
			ev.Event = value
			hasField = true
		case "data":
			data = append(data, value)
			hasField = true
		}
	}
}

func (p *Parser) readLine() (string, error) {
	var line []byte

	for {
		chunk, isPrefix, err := p.r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return "", io.ErrUnexpectedEOF
			}

			return "", err
		}

		line = append(line, chunk...)

		if len(line) > maxLine {
			return "", errors.New("event line is too long")
		}

		if !isPrefix {
			return string(line), nil
		}
	}
}
