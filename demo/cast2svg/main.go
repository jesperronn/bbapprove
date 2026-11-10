// Command cast2svg turns an asciicast v2 recording into one animated SVG, using freeze to draw each screen.
//
// Usage: cast2svg --in demo/demo.cast --out demo/demo.svg [--freeze PATH] [--frame N]
//
// Every distinct screen of the cast is drawn by freeze (`--language ansi`, fed on stdin). The frames are then
// stacked in one SVG that shows frame i from its cast timestamp to the next one, loops forever and holds the last
// frame for 2.5 s. The window chrome and the background come from frame 0 only. A viewer that does not run CSS
// animations shows frame 0. With --frame N only that frame is written, as a static SVG (for a quick look).
//
// Only the standard library is used; freeze is the one external tool.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// FinalHold is how long the last frame stays on screen before the loop restarts.
const FinalHold = 2.5

// FreezeVersion is the freeze release the demo assets are made with (the same pin as bin/demo).
const FreezeVersion = "v0.2.2"

// DefaultFont is the font stack of bin/demo shots: no font is embedded, the viewer picks the first installed family
// and the generic monospace keeps the columns aligned.
const DefaultFont = "JetBrains Mono, Menlo, Consolas, 'DejaVu Sans Mono', monospace"

// Frame is one distinct screen of the cast: the text with its SGR colours and nothing else, shown from At seconds.
type Frame struct {
	At   float64
	Text string
}

// Cast is the parsed recording: the terminal size and the distinct frames in time order. End is the timestamp of
// the last event, which can be later than the last frame when the screen did not change.
type Cast struct {
	Cols, Rows int
	Frames     []Frame
	End        float64
}

// Renderer draws one screen (ANSI text) as a complete SVG document.
type Renderer func(ansi string) ([]byte, error)

// ParseCast reads an asciicast v2 stream: a JSON header line, then one [time, "o", data] event per line. Each
// output event is applied to a screen buffer (a clear starts it over), and consecutive identical screens are merged.
func ParseCast(r io.Reader) (*Cast, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	if !sc.Scan() {
		return nil, errors.New("cast is empty: no header line")
	}
	var hdr struct {
		Version int `json:"version"`
		Width   int `json:"width"`
		Height  int `json:"height"`
	}
	if err := json.Unmarshal(sc.Bytes(), &hdr); err != nil {
		return nil, fmt.Errorf("cast header is not JSON: %w", err)
	}
	if hdr.Version != 2 || hdr.Width <= 0 || hdr.Height <= 0 {
		return nil, fmt.Errorf("cast header must be asciicast version 2 with a width and a height, got %s", sc.Text())
	}
	c := &Cast{Cols: hdr.Width, Rows: hdr.Height}
	var screen, last string
	for line := 2; sc.Scan(); line++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var ev []json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || len(ev) != 3 {
			return nil, fmt.Errorf("cast line %d is not an event [time, code, data]", line)
		}
		var at float64
		var code, data string
		if json.Unmarshal(ev[0], &at) != nil || json.Unmarshal(ev[1], &code) != nil {
			return nil, fmt.Errorf("cast line %d has a bad time or code", line)
		}
		if code != "o" {
			continue
		}
		if err := json.Unmarshal(ev[2], &data); err != nil {
			return nil, fmt.Errorf("cast line %d has bad output data", line)
		}
		if i := strings.LastIndex(data, "\x1b[2J"); i >= 0 {
			screen = data[i+len("\x1b[2J"):]
		} else {
			screen += data
		}
		if at > c.End {
			c.End = at
		}
		text := strings.TrimRight(Sanitize(screen), "\n")
		if len(c.Frames) > 0 && text == last {
			continue
		}
		last = text
		c.Frames = append(c.Frames, Frame{At: at, Text: text})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading the cast: %w", err)
	}
	if len(c.Frames) == 0 {
		return nil, errors.New("cast has no output events")
	}
	return c, nil
}

// Sanitize keeps the text and the SGR colour sequences of s and drops everything else a screen redraw carries:
// OSC sequences (the OSC 8 hyperlinks), cursor and clear sequences, other escapes, carriage returns and control
// characters. CRLF becomes LF; tabs stay.
func Sanitize(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		ch := s[i]
		switch {
		case ch == 0x1b && i+1 < len(s):
			switch next := s[i+1]; {
			case next == ']': // OSC ... BEL or ESC \
				j := i + 2
				for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
					j++
				}
				if j < len(s) && s[j] == 0x1b {
					j++
				}
				i = j + 1
			case next == '[': // CSI params intermediates final
				j := i + 2
				for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
					j++
				}
				if j < len(s) && s[j] == 'm' {
					b.WriteString(s[i : j+1])
				}
				i = j + 1
			case next == '(' || next == ')' || next == '*' || next == '+':
				i += 3
			default:
				i += 2
			}
		case ch == 0x1b, ch == '\r', ch < 0x20 && ch != '\n' && ch != '\t', ch == 0x7f:
			i++
		default:
			b.WriteByte(ch)
			i++
		}
	}
	return b.String()
}

var (
	rootTag = regexp.MustCompile(`(?s)<svg\b[^>]*>`)
	attrRe  = func(name string) *regexp.Regexp { return regexp.MustCompile(`\b` + name + `="([0-9.]+)(?:px)?"`) }
	widthRe = attrRe("width")
	heightR = attrRe("height")
	idDef   = regexp.MustCompile(`\bid="([^"]+)"`)
)

// parts is a freeze SVG split into the pieces the composition reuses.
type parts struct {
	w, h    string
	before  string // from the root tag to the content group: the background
	content string // the content group: the text of the frame
	after   string // after the content group: the window chrome and the defs
}

// split cuts a freeze document into its pieces. The content group is the first top-level <g>...</g>; a document
// without one (a stub) is all content.
func split(doc []byte) (parts, error) {
	s := string(doc)
	tag := rootTag.FindStringIndex(s)
	end := strings.LastIndex(s, "</svg>")
	if tag == nil || end < tag[1] {
		return parts{}, errors.New("freeze did not write an SVG document")
	}
	root := s[tag[0]:tag[1]]
	p := parts{w: "0", h: "0"}
	if m := widthRe.FindStringSubmatch(root); m != nil {
		p.w = m[1]
	}
	if m := heightR.FindStringSubmatch(root); m != nil {
		p.h = m[1]
	}
	inner := s[tag[1]:end]
	gs := strings.Index(inner, "<g ")
	if gs < 0 {
		gs = strings.Index(inner, "<g>")
	}
	if gs < 0 {
		p.content = inner
		return p, nil
	}
	ge := strings.Index(inner[gs:], "</g>")
	if ge < 0 {
		return parts{}, errors.New("freeze wrote an unclosed <g> element")
	}
	ge += gs + len("</g>")
	p.before, p.content, p.after = inner[:gs], inner[gs:ge], inner[ge:]
	return p, nil
}

// scopeIDs prefixes every id defined inside content (and the references to it) so frames never share one.
func scopeIDs(content, prefix string) string {
	for _, m := range idDef.FindAllStringSubmatch(content, -1) {
		id := m[1]
		content = strings.ReplaceAll(content, `id="`+id+`"`, `id="`+prefix+id+`"`)
		content = strings.ReplaceAll(content, `url(#`+id+`)`, `url(#`+prefix+id+`)`)
		content = strings.ReplaceAll(content, `href="#`+id+`"`, `href="#`+prefix+id+`"`)
	}
	return content
}

func pct(t, total float64) string {
	s := fmt.Sprintf("%.4f", t/total*100)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s + "%"
}

// Compose renders every frame with render and builds the animated SVG. With only >= 0 it writes that frame alone,
// as a static SVG.
func Compose(c *Cast, render Renderer, only int) ([]byte, error) {
	if only >= len(c.Frames) {
		return nil, fmt.Errorf("frame %d does not exist: the cast has %d frames", only, len(c.Frames))
	}
	var ps []parts
	for i, f := range c.Frames {
		if only >= 0 && i != only {
			ps = append(ps, parts{})
			continue
		}
		doc, err := render(f.Text)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}
		p, err := split(doc)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}
		ps = append(ps, p)
	}
	base := ps[0]
	if only >= 0 {
		base = ps[only]
	}
	total := c.End + FinalHold
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s">`+"\n", base.w, base.h, base.w, base.h)
	if only >= 0 {
		b.WriteString(base.before)
		b.WriteString(scopeIDs(base.content, "") + "\n")
		b.WriteString(base.after + "</svg>\n")
		return b.Bytes(), nil
	}
	b.WriteString("<style>\n.frame{opacity:0}\n.f0{opacity:1}\n")
	for i, f := range c.Frames {
		next := total
		if i+1 < len(c.Frames) {
			next = c.Frames[i+1].At
		}
		fmt.Fprintf(&b, ".f%d{animation:f%d-show %.3fs step-end infinite}\n", i, i, total)
		fmt.Fprintf(&b, "@keyframes f%d-show{", i)
		if i == 0 {
			fmt.Fprintf(&b, "0%%{opacity:1}")
		} else {
			fmt.Fprintf(&b, "0%%{opacity:0}%s{opacity:1}", pct(f.At, total))
		}
		if i+1 < len(c.Frames) {
			fmt.Fprintf(&b, "%s{opacity:0}", pct(next, total))
		}
		b.WriteString("}\n")
	}
	b.WriteString("</style>\n")
	b.WriteString(base.before)
	for i, p := range ps {
		fmt.Fprintf(&b, "<g class=\"frame f%d\" id=\"f%d-frame\">%s</g>\n", i, i, scopeIDs(p.content, fmt.Sprintf("f%d-", i)))
	}
	b.WriteString(base.after + "</svg>\n")
	return b.Bytes(), nil
}

// Size is the freeze window size in pixels for a terminal of cols x rows at freeze's 14 px font: the columns and
// two more for margin (as the earlier renderer did), the rows, the padding and the window bar.
func Size(cols, rows int) (w, h int) {
	return int(math.Ceil(float64(cols+2)*8.4)) + 40, int(math.Ceil(float64(rows)*16.8)) + 90
}

// FreezeRenderer runs the freeze binary on each frame, with a fixed size so all frames match.
func FreezeRenderer(freeze, font string, w, h int) Renderer {
	return func(ansi string) ([]byte, error) {
		dir, err := os.MkdirTemp("", "cast2svg-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		out := filepath.Join(dir, "frame.svg")
		cmd := exec.Command(freeze, "--language", "ansi", "--font.family", font, "--window",
			"--width", fmt.Sprint(w), "--height", fmt.Sprint(h), "-o", out)
		cmd.Stdin = strings.NewReader(ansi)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Stdout = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("freeze (%s) failed: %w: %s", freeze, err, strings.TrimSpace(stderr.String()))
		}
		return os.ReadFile(out)
	}
}

func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("cast2svg", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "", "the asciicast v2 file to read")
	out := fs.String("out", "", "the SVG file to write")
	freeze := fs.String("freeze", "freeze", "the freeze binary (a name on PATH or a path)")
	font := fs.String("font", DefaultFont, "the font stack of the frames")
	frame := fs.Int("frame", -1, "write only frame N, as a static SVG")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("cast2svg needs --in and --out")
	}
	path, err := exec.LookPath(*freeze)
	if err != nil {
		return fmt.Errorf("freeze is not installed (%s): install it with go install github.com/charmbracelet/freeze@%s and put it on PATH or pass --freeze", err, FreezeVersion)
	}
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()
	c, err := ParseCast(f)
	if err != nil {
		return err
	}
	w, h := Size(c.Cols, c.Rows)
	svg, err := Compose(c, FreezeRenderer(path, *font, w, h), *frame)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, svg, 0o644); err != nil {
		return err
	}
	if *frame < 0 {
		fmt.Fprintf(stderr, "cast2svg: %d frames, %.1f s loop, %d bytes\n", len(c.Frames), c.End+FinalHold, len(svg))
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "cast2svg:", err)
		os.Exit(1)
	}
}
