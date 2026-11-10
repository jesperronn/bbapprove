package app

import (
	"strings"

	"bbapprove/internal/style"
)

// helpTopic is one page of `bbapprove help TOPIC`, written in the markup of package style.
type helpTopic struct{ name, summary, markup string }

// helpTopics lists every topic in the order `bbapprove help` shows them. The README must point at each
// one (see docs_test.go), so a new topic cannot be forgotten there.
var helpTopics = []helpTopic{
	{"rules", "which PRs are eligible, the ignore list, the STATUS column", rulesMarkup},
	{"tui", "the interactive TUI: columns, status bar, exit summary, mouse", tuiMarkup},
	{"config", "config file lookup, precedence, first run, --init, --print-config", configMarkup},
	{"token", "where the API token comes from", tokenMarkup},
	{"update", "--outdated and --update", updateMarkup},
	{"env", "environment variables and exit codes", envMarkup},
	{"doctor", "bbapprove doctor, the diagnostics report", doctorUsageMarkup},
}

// topicsMarkup is the "Topics:" block shared by the top-level help and `bbapprove help`.
func topicsMarkup() string {
	var b strings.Builder
	b.WriteString("{h:Topics:} bbapprove {cmd:help} {val:TOPIC}\n")
	for _, t := range helpTopics {
		pad := strings.Repeat(" ", 8-len(t.name))
		b.WriteString("  {cmd:" + t.name + "}" + pad + t.summary + "\n")
	}
	return b.String()
}

// topicUsageFor renders one topic with the given theme.
func topicUsageFor(t style.Theme, name string) (string, bool) {
	for _, h := range helpTopics {
		if h.name == name {
			return t.MustRender(h.markup), true
		}
	}
	return "", false
}

// topicListFor renders the title and the topic list: `bbapprove help` without a topic.
func topicListFor(t style.Theme, version string) string {
	return t.MustRender(helpTitle(version) + "\n" + topicsMarkup())
}

const rulesMarkup = `{title:bbapprove help rules — which PRs are eligible and how every row is labelled.}

{h:Eligible} when ALL hold:
  - author email is the configured {val:author_email} (config file or {env:BBAPPROVE_AUTHOR_EMAIL}; no default)
  - title starts with "chore(deps):" or "fix(deps):"
  - title does not mention a disallowed dependency (case-insensitive substring; config "disallow",
    {flag:--disallow}, {env:BBAPPROVE_DISALLOW}, {env:BBAPPROVE_DISALLOW_FILE}); see {cmd:help} config
  - update is minor/patch/digest (a title ending "to vN", or a branch renovate/major-*, counts as major;
    this heuristic is not reliable yet, see TODO.md)
  - not already approved by you
  - not on your ignore list
Anything else is reported as SKIP with the reason. Nothing is changed without {flag:--apply}.

{h:Ignore list:} a personal file, {path:$XDG_CONFIG_HOME/bbapprove/ignore} (default {path:~/.config/bbapprove/ignore}),
one dependency per line, # comments allowed. It is separate from the config's disallow list: disallow is a
hard block shown as SKIP; ignored PRs are shown dim as IGNORED ("on your ignore list: ENTRY") and are never
approved, merged, declined or commented, in a dry run and with {flag:--apply}. In the TUI, ignored rows are
hidden on demand and i edits the list. A title matching an entry (case-insensitive substring) is IGNORED
whatever else applies; removing the entry restores the PR's own SKIP reason. A missing file is an empty list;
a file that exists but cannot be read stops the run (exit 1) rather than risk acting on an ignored PR.
{cmd:doctor} {flag:-v} repeats this.
{flag:--hide-ignored} leaves ignored rows out of the table. The footer names the file:
"N ignored via FILE (see: bbapprove doctor)", or "N ignored (hidden) via FILE" with {flag:--hide-ignored}.

{h:STATUS column:} blank = eligible and untouched; SKIP (reason after the title); IGNORED; in flight
APPROVING… DECLINING… COMMENTING…; done MERGED DECLINED COMMENTED; failures NOT MERGED (approved, merge
failed) and FAIL. A later action on a row replaces its word. A comment never makes a row ineligible.
`

const tuiMarkup = `{title:bbapprove help tui — the interactive TUI (-i).}

Keys: ? in the TUI, or the README. Lowercase keys act on the cursor row, uppercase keys on the marked rows.
The TUI stays open after every action; only q / ctrl+c quits. {flag:--no-mouse} or {env:BBAPPROVE_MOUSE}=0 turns mouse capture off.

{h:Columns:} STATUS (see {cmd:help} rules), then
  🔨 build        ✓ passed, ✗ failed, … running, ? unknown or none
  🔎 SonarQube    ✓ quality gate passed, ✗ failed, ? unknown
  👍 approval     ✓ approved (by someone), ✗ needs work (a reviewer requested changes), – no reviews yet
  💬              comment count (tasks excluded)
  ✅              open tasks
  PROJECT/repo#id the PR reference, then the title

{h:Look:} the cursor row has a subtle full-width background and the › marker; marked rows show a bold yellow ▌ bar;
the mouse-hovered row a fainter background. With {env:NO_COLOR} only the markers remain.

{h:Status bar:} two lines that never wrap. Line 1 is the transient message or active prompt, otherwise the cursor
row's PR URL (where o goes) with the version dim at the right; on narrow terminals the URL is shortened in the
middle (https://host/…/pull-requests/4961) so the PR number and the "o open" hint stay visible. Line 2 is
"N PRs, M marked" and as many key hints as fit.

{h:Hints follow the Shift key:} by default line 2 shows the instant keys:
  a approve  d decline  c comment  i ignore  x mark  o open  ? help  q quit
While you HOLD Shift it switches to the batch keys, and releasing Shift restores the instant set:
  A approve marked  D decline marked  C comment marked  ctrl+a mark all  x mark  o open  ? help  q quit
Neither set wraps: trailing items are dropped to fit, and "o open" is the last one dropped before "? help" and
"q quit". The ? screen always lists both sets. This needs a terminal that speaks the kitty keyboard protocol
(Ghostty, kitty, recent iTerm2, WezTerm, foot, Alacritty): bbapprove requests the enhancements (key release
events, all keys as escape codes with associated text and alternate keys) and detects support from the
terminal's reply. In any other terminal (Terminal.app, tmux without passthrough, ...) the hints stay on the
instant set and every key works the same. The Shift state is also cleared when the window loses focus or a key
arrives without Shift, in case a release is lost. {env:BBAPPROVE_KITTY}=0 stops bbapprove requesting the
enhancements (the hints never change; Bubble Tea still asks for basic key disambiguation).

{h:Exit summary:} on q / ctrl+c the TUI prints what was done this session to the scrollback: a table in the CLI
format with only the actions taken (MERGED, NOT MERGED, DECLINED, COMMENTED, FAIL, IGNORED / UN-IGNORED), then a
tally such as "3 merged, 1 declined, 1 failed" (zero counts omitted), or "No changes made." Actions still
running are listed as WORKING… and the tally ends "(still running)": quitting does not wait for them. One line
per PR and kind of action. Cut to the terminal width on a TTY, never truncated when piped.

{h:Mouse:} on by default: click a row to move the cursor, click the first three columns to toggle its mark,
wheel scrolls 3 rows, hover highlights. Click the PR URL in the footer (or the "o open" hint) to open it, the same as
pressing o. Clicks elsewhere outside the rows are ignored, and the mouse is ignored while a text prompt is open.
Capturing the mouse disables the terminal's native text selection and cmd-click on links; hold Shift to bypass it
in most terminals (Ghostty, iTerm2, kitty).
`

const configMarkup = `{title:bbapprove help config — the config file and where each setting comes from.}

A TOML file; {path:bbapprove.config.template} lists every key (type, repository, author_email, disallow, update_repo,
user_token_name, delete_source_branch) with comments. Nothing site- or person-specific is built in. {flag:--print-config} shows the
effective settings and which of flag, env, file or default each came from (never a secret).

{h:Lookup:} the first that exists wins: {flag:--config} {val:FILE}, {env:BBAPPROVE_CONFIG} (both must exist), {path:<repo>/bbapprove.config.toml}
(the parent of the binary's directory, like adapters/; git-ignored), then {path:$XDG_CONFIG_HOME/bbapprove/config.toml}
(default {path:~/.config/bbapprove/config.toml}).

{h:Precedence} per setting: command line > environment > file > built-in default. Environment names:
{env:BBAPPROVE_REPOSITORY}, {env:BBAPPROVE_AUTHOR_EMAIL}, {env:BBAPPROVE_TOKEN_NAME}, {env:BBAPPROVE_UPDATE_REPO}. Only
type ("bitbucket"), update_repo and delete_source_branch have defaults; repository and author_email are required and bbapprove exits 1
naming the missing one and the paths searched. disallow is the union of the file, {flag:--disallow} /
{env:BBAPPROVE_DISALLOW} (comma-separated) and the optional {env:BBAPPROVE_DISALLOW_FILE} (one dependency per line;
default {path:disallow.txt} in the repo root, git-ignored). {env:BBAPPROVE_ADAPTER} (an executable path) overrides what type selects.
{h:delete_source_branch} (default true) deletes the PR's source branch after a successful merge; set it to false
in the file or with {env:BBAPPROVE_DELETE_SOURCE_BRANCH} (true, false, 1, 0, yes, no; there is no command-line flag).
Unknown keys only warn; malformed TOML stops the run and names the file and line.

{h:First run:} with no config file, a run on a terminal offers to create {path:$XDG_CONFIG_HOME/bbapprove/config.toml}
(mode 0600, never overwrites) from the template built into the binary, opens it in {env:$VISUAL}/{env:$EDITOR}, re-prompts
while the example values are unchanged (at most 3 rounds), then continues with your original command. In CI or
pipes nothing is written; the error shows the mkdir and cp commands to run.
{flag:--init} does the same on request: it writes to {flag:--config} {val:FILE}, else {env:BBAPPROVE_CONFIG}, else the default path, and
refuses to replace an existing file unless {flag:--force}.
`

const tokenMarkup = `{title:bbapprove help token — where the API token comes from.}

Resolved in this order:
  1. {env:BBAPPROVE_TOKEN}, when set and non-empty. It wins; 1Password is not consulted.
  2. The 1Password item named by {flag:--token-name} {val:NAME}, {env:BBAPPROVE_TOKEN_NAME} or user_token_name (that precedence),
     looked up with: op item get NAME --reveal --fields label=token, then label=credential if that fails
     or is empty. There is no default item name.
  3. Neither: every run that talks to the forge (dry run, {flag:--apply}, {flag:-i}) stops at once with exit 1, before the
     adapter is called. {flag:--help}, {flag:--print-config}, {flag:--init}, {flag:--outdated}, {flag:--update} and {cmd:doctor} need no token;
     doctor flags a missing source as a problem and never looks the item up.

Create a Bitbucket Server / Data Center HTTP access token for the account that reviews the PRs: repository read
access (dashboard, build status, PR details) plus write access to approve, merge, comment on and decline.

There is no {flag:--token} option: a token on the command line is visible in the process list and shell history.
`

const updateMarkup = `{title:bbapprove help update — check for and install a newer release.}

bbapprove never checks by itself. Neither flag needs a config file, token or forge access; both are exclusive
with {flag:--apply} and PR URLs.

{flag:--outdated} [{flag:--pre}]  read-only check. Exit 0 up to date, 1 outdated, 2 could not check.
  Asks "gh release list" for the newest release; in a git checkout without gh it falls back to
  "git ls-remote --tags origin". A pre-release build compares against the newest release including
  pre-releases; a stable build against the newest stable one unless {flag:--pre}. The repository defaults to
  jesperronn/bbapprove; override with update_repo or {env:BBAPPROVE_UPDATE_REPO}.
{flag:--update} [{flag:--yes}] [{flag:--pre}]  runs the same check, prints a plan and asks "Proceed? [y/N]" (default no).
  {flag:--yes} skips the question; without a terminal it refuses to change anything unless you pass {flag:--yes}.
  Git checkout: git fetch --tags, git pull --ff-only, bin/build; needs a clean tree on main that can be
  fast-forwarded, otherwise nothing is changed.
  Release archive: the new release is unpacked next to the current install as bbapprove-<tag>-<os>-<arch>
  after its SHA-256 is verified against the release's SHA256SUMS (needs gh). The running tree is never
  modified and nothing is switched automatically: the command prints how to switch and how to roll back
  (ln -sfn lines when your bbapprove is a symlink). Your config in {path:$XDG_CONFIG_HOME/bbapprove/config.toml} is shared
  by both trees.
  Windows (release zip): the folder holding the running {path:bbapprove.exe} is updated in place, because
  Windows has no usable symlink and cannot overwrite a running program. The zip bbapprove-<tag>-windows-amd64.zip
  is verified the same way and unpacked next to the install folder, then {path:bbapprove.exe} is renamed to
  {path:bbapprove.exe.old} and the new one takes its place, followed by the template, README, CHANGELOG and
  LICENSE. If a step fails the old files are restored. The new version starts the next time you run bbapprove;
  delete {path:bbapprove.exe.old} when you no longer need it. Your config in {path:%AppData%\bbapprove} is never touched.

SHA256SUMS comes from the same release as the archive, so the check protects against corruption and partial
downloads, not a compromised release. Provenance and attestation checks are future work (see TODO.md).
`

const envMarkup = `{title:bbapprove help env — environment variables and exit codes.}

{h:Environment}
  {env:BBAPPROVE_TOKEN}          API token; wins over a 1Password item ({cmd:help} token)
  {env:BBAPPROVE_CONFIG}         config file path (also {flag:--config}); must exist
  {env:BBAPPROVE_REPOSITORY}     forge base URL (overrides repository)
  {env:BBAPPROVE_TOKEN_NAME}     1Password item name, used only when {env:BBAPPROVE_TOKEN} is unset (also {flag:--token-name}; no default)
  {env:BBAPPROVE_AUTHOR_EMAIL}   author the eligibility check requires (overrides author_email)
  {env:BBAPPROVE_UPDATE_REPO}    GitHub owner/repo for {flag:--outdated} and {flag:--update} (default jesperronn/bbapprove)
  {env:BBAPPROVE_DELETE_SOURCE_BRANCH}  true (default) or false: delete the source branch after a merge (overrides delete_source_branch)
  {env:BBAPPROVE_DISALLOW}       comma-separated extra disallow items
  {env:BBAPPROVE_DISALLOW_FILE}  optional extra disallow file (default disallow.txt in the repo root)
  {env:BBAPPROVE_JOBS}           parallel PR fetches and merges (default: CPU count)
  {env:BBAPPROVE_ADAPTER}        forge adapter executable (default adapters/bitbucket)
  {env:BBAPPROVE_MOUSE}          0 turns TUI mouse capture off (same as {flag:--no-mouse})
  {env:BBAPPROVE_KITTY}          0 stops the TUI requesting the kitty keyboard protocol
  {env:NO_COLOR}, {env:FORCE_COLOR}  colour only on a terminal; NO_COLOR turns it off and wins, FORCE_COLOR=1 forces it on
  {env:XDG_CONFIG_HOME}          base of the config and ignore paths (default ~/.config)
  {env:VISUAL}, {env:EDITOR}         editor opened by the first-run setup

{h:Adapter side} (read by adapters/bitbucket; the core sets BASE_URL, TOKEN_NAME and DELETE_SOURCE_BRANCH from the configuration)
  {env:BBAPPROVE_BASE_URL}       forge base URL (required)
  {env:BBAPPROVE_TOKEN_NAME}     1Password item for "auth token"
  {env:BBAPPROVE_DELETE_SOURCE_BRANCH}  true (default when unset) or false: delete the source branch after a merge
  {env:BBAPPROVE_MERGE_RETRIES}  merge retries when the PR version went stale (default 4)
  {env:BBAPPROVE_RETRY_SLEEP}    seconds between retries (default 2)
  {env:BBAPPROVE_TOKEN} is required by the "pr" commands (exit 2 when unset).

{h:Exit codes}
  0  ok (doctor: warnings allowed; --outdated: up to date)
  1  a failure (doctor: a problem was found; --outdated: a newer release exists)
  2  bad usage (--outdated: could not check)
  127  (launcher) Go is missing and no binary is built
`

// doctorUsageMarkup is the help for `bbapprove doctor`, in the markup of package style.
const doctorUsageMarkup = `{title:bbapprove doctor — diagnose the installation and the configuration.}

{h:Usage:} bbapprove {cmd:doctor} [{flag:-v}] [{flag:--offline}] [{flag:--config} {val:FILE}] [{flag:--disallow} {val:a,b}]
                         [{flag:--token-name} {val:NAME}]
       bbapprove {cmd:doctor} {flag:--help}

Read-only: prints a report, changes nothing, never prints a secret, and needs no token,
forge access or config file. It also works when the configuration is broken: that is what it reports.

{h:Options:}
  {flag:-v}              the full report: the default report plus the install root, the config lookup order and
                       file mode, where each disallow entry comes from, the environment and the tool paths
  {flag:--offline}       skip the update check (nothing leaves this machine)
  {flag:--config} {val:FILE}   diagnose this config file ({env:BBAPPROVE_CONFIG} does the same; the flag wins)
  {flag:--disallow} {val:a,b}  add disallowed dependencies, as for a normal run
  {flag:--token-name} {val:NAME}  1Password item that holds the token, as for a normal run ({env:BBAPPROVE_TOKEN_NAME}
                         and user_token_name do the same); only used when {env:BBAPPROVE_TOKEN} is unset
  {flag:-h}, {flag:--help}      show this help

{h:Sections:} one line each, in this order. A problem (warn or FAIL) is always printed, with what to do
about it, and the last line only counts them; no fact is printed twice.
  version      bbapprove version, platform, git checkout or release archive, the running binary
               ({flag:-v}: and the install root); warn when the bbapprove on your PATH is another binary
  update       asks gh (or git in a checkout) for a newer release; skipped with {flag:--offline}
  config       the file in use and how it was found ({flag:--config}, {env:BBAPPROVE_CONFIG}, repo root,
               {path:$XDG_CONFIG_HOME/bbapprove/config.toml}), that it is valid TOML and not still the
               template's example values; {flag:-v} adds its mode and the state of all four candidates
               (used, not set, not found, shadowed). Then every effective setting with its source:
               {src-flag:[flag]}, {src-env:[env]}, {src-file:[file]} or {src-default:[default]}; with {flag:-v} the disallow
               row names where its entries come from. A forge adapter that is missing or not executable
               is reported under the forge type
  token        where the token comes from: {env:BBAPPROVE_TOKEN} (wins), else the 1Password item named by
               {flag:--token-name} / {env:BBAPPROVE_TOKEN_NAME} / user_token_name; never a value, and doctor
               does not run op. FAIL when there is neither
  ignore       the per-user ignore file, its entry count and mode; an unreadable list is a problem
               (bbapprove then refuses to run); {flag:-v} adds what the list does
  disallow     entries from {env:BBAPPROVE_DISALLOW} and the disallow file, when there are any (with {flag:-v}
               they are part of the settings row instead)
  env          the {env:BBAPPROVE_*} variables that are set, plus {env:NO_COLOR} and {env:FORCE_COLOR}; {flag:-v} prints
               values and adds {env:XDG_CONFIG_HOME}, {env:EDITOR} and {env:VISUAL}, but not what the settings show
  tools        git, gh and op on PATH (with their paths for {flag:-v}), and whether gh is logged in

{h:Markers:} {ok:ok} / {ok:✓} fine, {warn:warn} / {warn:!} worth a look, {fail:FAIL} / {fail:✗} a problem.
The symbols replace the words on a colour terminal.

{h:Exit:} 0 all good (warnings allowed), 1 a problem was found, 2 bad usage.

Colour only on a terminal; {env:NO_COLOR} turns it off, {env:FORCE_COLOR}=1 forces it on.
`

// doctorUsageFor renders the doctor help with the given theme.
func doctorUsageFor(t style.Theme) string { return t.MustRender(doctorUsageMarkup) }

// doctorUsage returns the plain doctor help.
func doctorUsage() string { return doctorUsageFor(style.Theme{}) }
