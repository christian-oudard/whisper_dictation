#!/usr/bin/env zsh
# Check that the completion still reads the menus it parses.
#
# _diktat keeps no list of its own: the commands come out of `diktat --help`
# and the two menus out of `diktat model` and `diktat tx-model`. That is the
# right trade -- a copied list drifts, and this one did -- but it moves the
# breakage from a stale list to a silent parse, where a column that grew by a
# character means tab does nothing and says nothing.
#
# So run the parsing against a real binary. The completion system is not here,
# and does not need to be: what can break is the awk, so _describe is replaced
# by something that keeps what it was handed.
#
#     zsh completions/check.zsh [path to diktat]
set -u

bin=${1:-diktat}
whence -p -- $bin > /dev/null || { print -u2 "$bin is not on PATH"; exit 1 }

_arguments() { return 0 }
_files() { return 0 }
typeset -ga captured
_describe() {
    while [[ $1 == -* ]]; do shift; done
    captured=(${(P)2})
}

# Sourcing the file runs _diktat once, which reads $state to decide what to
# complete. Nothing is being completed here, so give it one.
typeset -g state=

alias diktat="$bin"
source ${0:a:h}/_diktat

fails=0
complain() { print -u2 "FAIL: $*"; (( fails++ )) }

# Commands: every one the binary lists, each with the summary beside it.
_diktat_commands
for want in daemon toggle repeat model transcribe tx-model version; do
    (( ${captured[(I)$want:?*]} )) || complain "no command $want with a summary: ${captured}"
done

# Models: the menu names, and nothing from the header row or the notes under
# the table.
_diktat_models
(( $#captured >= 5 )) || complain "only $#captured models: ${captured}"
(( ${captured[(I)parakeet-tdt_ctc-110m]} )) || complain "the default model is not offered: ${captured}"
for name in $captured; do
    [[ $name == [A-Za-z]* ]] || complain "not a model name: $name"
done

# Pipelines: offered by number, described by name, and the name stops before
# the size column however long it is.
_diktat_tx_models
(( $#captured >= 3 )) || complain "only $#captured pipelines: ${captured}"
for entry in $captured; do
    [[ $entry == <->:[A-Za-z]* ]] || complain "not a numbered pipeline: $entry"
    [[ $entry != *[0-9]" "[KMG]iB* ]] || complain "the size leaked into the name: $entry"
done

(( fails )) && exit 1
print "completion reads all three menus"
