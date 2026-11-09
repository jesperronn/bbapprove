package app

import "bbapprove/internal/style"

// doctorUsageMarkup is the help for `bbapprove doctor`, in the markup of package style.
const doctorUsageMarkup = `{title:bbapprove doctor — diagnose the installation and the configuration.}

{h:Usage:} bbapprove {cmd:doctor} [{flag:--offline}] [{flag:--config} {val:FILE}] [{flag:--disallow} {val:a,b}]
       bbapprove {cmd:doctor} {flag:--help}

Read-only: prints a report, changes nothing, never prints a secret, and needs no token,
forge access or config file. It also works when the configuration is broken: that is what it reports.

{h:Options:}
  {flag:--offline}       skip the update check (nothing leaves this machine)
  {flag:--config} {val:FILE}   diagnose this config file ({env:BBAPPROVE_CONFIG} does the same; the flag wins)
  {flag:--disallow} {val:a,b}  add disallowed dependencies, as for a normal run
  {flag:-h}, {flag:--help}      show this help

{h:Sections:} one block each, in this order
  version      bbapprove version, platform, git checkout or release archive, the running binary,
               and whether the bbapprove on your PATH is that binary
  update       asks gh (or git in a checkout) for a newer release; skipped with {flag:--offline}
  config       every candidate config file ({flag:--config}, {env:BBAPPROVE_CONFIG}, repo root,
               {path:$XDG_CONFIG_HOME/bbapprove/config.toml}) and which one wins, its mode, whether it is
               valid TOML and still holds the template's example values; then every effective
               setting with its source: {src-flag:[flag]}, {src-env:[env]}, {src-file:[file]} or {src-default:[default]}
  adapter      the forge type, the adapter path, and that it exists and is executable
  token        whether {env:BBAPPROVE_TOKEN} is set (never its value, only its length)
  ignore list  the per-user ignore file, whether it is readable (an unreadable list is a problem:
               bbapprove then refuses to run), and its entries
  disallow     the disallow lists from the config file, {env:BBAPPROVE_DISALLOW} and the disallow file
  environment  the {env:BBAPPROVE_*} variables that are set, plus {env:NO_COLOR}, {env:FORCE_COLOR},
               {env:XDG_CONFIG_HOME}, {env:EDITOR} and {env:VISUAL}
  tools        git and gh on PATH, and whether gh is logged in

{h:Markers:} {ok:ok} / {ok:✓} fine, {warn:warn} / {warn:!} worth a look, {fail:FAIL} / {fail:✗} a problem.
The symbols replace the words on a colour terminal.

{h:Exit:} 0 all good (warnings allowed), 1 a problem was found, 2 bad usage.

Colour only on a terminal; {env:NO_COLOR} turns it off, {env:FORCE_COLOR}=1 forces it on.
`

// doctorUsageFor renders the doctor help with the given theme.
func doctorUsageFor(t style.Theme) string { return t.MustRender(doctorUsageMarkup) }

// doctorUsage returns the plain doctor help.
func doctorUsage() string { return doctorUsageFor(style.Theme{}) }
