// Command tally counts tool use in gila's --json output, per model: calls, failures and output
// bytes for each tool, with bash split by the programs it ran. See docs/dev/tools.md.
//
//	gila -p "task" --json > run.jsonl
//	go run ./scripts/tally run.jsonl [more.jsonl ...]
//
// With no files it reads stdin. A run's records are attributed to the model in the result
// record that ends it.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
)

type record struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Label    string `json:"label"`
	OK       bool   `json:"ok"`
	Output   string `json:"output"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type key struct{ model, tool string }

type count struct{ calls, failed, bytes int }

func main() {
	var inputs []io.Reader
	for _, name := range os.Args[1:] {
		f, err := os.Open(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		inputs = append(inputs, f)
	}
	if len(inputs) == 0 {
		inputs = []io.Reader{os.Stdin}
	}
	totals := map[key]*count{}
	for _, r := range inputs {
		if err := tally(r, totals); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	report(os.Stdout, totals)
}

// tally adds r's tool results to totals. Results are held until the run's result record names
// the model; a run cut off before it counts under "unknown".
func tally(r io.Reader, totals map[key]*count) error {
	var pending []record
	flush := func(model string) {
		for _, rec := range pending {
			k := key{model, toolName(rec)}
			c := totals[k]
			if c == nil {
				c = &count{}
				totals[k] = c
			}
			c.calls++
			if !rec.OK {
				c.failed++
			}
			c.bytes += len(rec.Output)
		}
		pending = nil
	}
	sc := bufio.NewScanner(r)
	// A record carries a whole tool result, up to the output cap plus JSON escaping.
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for n := 1; sc.Scan(); n++ {
		var rec record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
		switch rec.Type {
		case "tool_result":
			pending = append(pending, rec)
		case "result":
			flush(rec.Provider + "/" + rec.Model)
		}
	}
	flush("unknown")
	return sc.Err()
}

// toolName is the tool, or for bash "bash " plus the programs the command runs, joined by "+".
func toolName(rec record) string {
	if rec.Name != "bash" {
		return rec.Name
	}
	return "bash " + strings.Join(programs(strings.TrimPrefix(rec.Label, "$ ")), "+")
}

var (
	assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	// redirection matches a word that opens a redirection, such as "<", "2>" or ">out"; a bare
	// operator takes the next word as its target.
	redirection     = regexp.MustCompile(`^[0-9]*(&>|<|>)`)
	bareRedirection = regexp.MustCompile(`^[0-9]*(&>|<<?|>>?)&?$`)
	// skipWord precedes the program in a simple command: keywords, negation, and wrappers that
	// run the next word.
	skipWord = set("if", "then", "else", "elif", "while", "until", "do", "done", "fi", "esac",
		"!", "{", "}", "time", "env", "nohup", "sudo", "exec", "command")
	// header starts a simple command that runs nothing itself, such as "for f in a b".
	header = set("for", "select", "case", "function")
	// builtin names a shell builtin that only prints or sets state. It counts only when a
	// command runs nothing else, as in "echo hi".
	builtin = set("cd", "echo", "printf", "true", "false", ":", "export", "set", "unset", "local",
		"read", "shift", "test", "[", "[[", "return", "exit", "wait", "source", ".", "trap")
)

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// programs returns the programs a command runs, sorted and unique: the first word of each
// simple command, after keywords, assignments and wrappers, without builtins. It is a sketch
// of shell parsing, enough to see through loops, chains and pipelines, not a parser.
func programs(cmd string) []string {
	found := map[string]bool{}
	firstBuiltin := ""
	for _, words := range simpleCommands(cmd) {
	scan:
		for i := 0; i < len(words); i++ {
			w := words[i]
			switch {
			case skipWord[w] || assignment.MatchString(w):
				continue
			case redirection.MatchString(w):
				if bareRedirection.MatchString(w) {
					i++
				}
				continue
			case header[w]:
				break scan
			case builtin[w]:
				if firstBuiltin == "" {
					firstBuiltin = w
				}
				break scan
			default:
				found[path.Base(w)] = true
				break scan
			}
		}
	}
	if len(found) == 0 {
		if firstBuiltin != "" {
			return []string{firstBuiltin}
		}
		return []string{"(empty)"}
	}
	out := make([]string, 0, len(found))
	for p := range found {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// simpleCommands splits cmd into words, grouped into simple commands at unquoted ; & | ( )
// and newlines. Quotes are removed, and heredoc bodies are skipped, so neither a quoted
// separator nor a line of a heredoc starts a command.
func simpleCommands(cmd string) [][]string {
	var (
		cmds        [][]string
		words       []string
		word        strings.Builder
		inWord      bool
		quote       rune
		heredoc     string // delimiter of a heredoc whose body starts at the next newline
		wantHeredoc bool   // the next word is a heredoc delimiter
	)
	endWord := func() {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		switch {
		case wantHeredoc:
			heredoc, wantHeredoc = w, false
		case w == "<<" || w == "<<-":
			wantHeredoc = true
		case strings.HasPrefix(w, "<<"):
			heredoc = strings.TrimPrefix(strings.TrimPrefix(w, "<<"), "-")
		default:
			words = append(words, w)
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
		}
		words = nil
	}
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' && i+1 < len(rs) {
				i++
				word.WriteRune(rs[i])
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == '\\' && i+1 < len(rs):
			i++
			word.WriteRune(rs[i])
			inWord = true
		case r == '\n':
			endCmd()
			if heredoc != "" {
				// Skip the body up to the line holding only the delimiter.
				rest := string(rs[i+1:])
				skip := len(rest)
				for off := 0; off < len(rest); {
					line, _, _ := strings.Cut(rest[off:], "\n")
					if strings.TrimSpace(line) == heredoc {
						skip = off + len(line)
						break
					}
					off += len(line) + 1
				}
				i += len([]rune(rest[:skip]))
				heredoc = ""
			}
		case redirect(rs, i):
			word.WriteRune(r)
			inWord = true
		case r == ';' || r == '&' || r == '|' || r == '(' || r == ')':
			endCmd()
		case r == ' ' || r == '\t':
			endWord()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	endCmd()
	return cmds
}

// redirect reports whether the & or | at rs[i] belongs to a redirection such as 2>&1, &> or
// >|, where it separates nothing.
func redirect(rs []rune, i int) bool {
	r := rs[i]
	prev := i > 0 && (rs[i-1] == '>' || rs[i-1] == '<')
	next := i+1 < len(rs) && rs[i+1] == '>'
	return r == '&' && (prev || next) || r == '|' && i > 0 && rs[i-1] == '>'
}

func report(w io.Writer, totals map[key]*count) {
	byModel := map[string]int{}
	var keys []key
	for k, c := range totals {
		keys = append(keys, k)
		byModel[k.model] += c.bytes
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.model != b.model {
			return a.model < b.model
		}
		if totals[a].bytes != totals[b].bytes {
			return totals[a].bytes > totals[b].bytes
		}
		return a.tool < b.tool
	})
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "model\ttool\tcalls\tfailed\toutput bytes\tshare\t")
	for _, k := range keys {
		c := totals[k]
		share := 0
		if t := byModel[k.model]; t > 0 {
			share = c.bytes * 100 / t
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d%%\t\n", k.model, k.tool, c.calls, c.failed, c.bytes, share)
	}
	tw.Flush()
}
