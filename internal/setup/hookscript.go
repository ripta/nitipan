package setup

import (
	"fmt"
	"strings"
)

// hookScript renders the reference-transaction hook. executable is baked in as a single-quoted
// word, and chained is a shell expression for the hook to run after nitipan's check.
//
// The script tells whether a transaction touches a branch or HEAD in the prepared state without
// starting any other program. A rebase of fifty commits runs the hook hundreds of times for refs
// such as ORIG_HEAD and AUTO_MERGE, and in the committed and aborted states too, so only the updates
// that can collide pay for the binary, and the rest pay for one shell.
func hookScript(executable, chained string) ([]byte, error) {
	if strings.ContainsRune(executable, '\n') {
		return nil, fmt.Errorf("nitipan path %q: want a path without a newline", executable)
	}

	text := strings.ReplaceAll(hookTemplate, "@@NITIPAN@@", shellQuote(executable))
	text = strings.ReplaceAll(text, "@@CHAINED@@", chained)

	return []byte(text), nil
}

const hookTemplate = `#!/bin/sh
# nitipan-managed hook
# Installed by nitipan setup git --install. Removed by nitipan setup git --uninstall, which also
# puts back the hook this one replaced. It refuses a branch or HEAD update that a nitipan lease
# forbids, then runs the hook it replaced, if any.

here=${0%/*}
nitipan=@@NITIPAN@@
chained=@@CHAINED@@

# Only the prepared state can refuse, so the rest go straight to the chained hook, or end here.
if [ "$1" != prepared ]; then
	if [ -x "$chained" ]; then
		exec "$chained" "$@"
	fi

	exit 0
fi

input=
judged=0

while IFS=' ' read -r old new ref; do
	input="$input$old $new $ref
"

	case $ref in
	HEAD | refs/heads/*)
		judged=1
		;;
	esac
done

if [ "$judged" = 1 ]; then
	if [ ! -x "$nitipan" ]; then
		nitipan=$(command -v nitipan 2>/dev/null) || nitipan=
	fi

	if [ -z "$nitipan" ]; then
		echo "nitipan: git refused to update a branch or HEAD, because the nitipan binary that checks leases is missing." >&2
		echo "Reinstall nitipan so it is on PATH, or ask the user to run: nitipan setup git --uninstall <checkout>" >&2
		exit 1
	fi

	printf '%s' "$input" | "$nitipan" hook reference-transaction prepared
	status=$?

	if [ "$status" -eq 2 ]; then
		echo "nitipan: git refused to update a branch or HEAD, because $nitipan is too old for the hook installed in this clone." >&2
		echo "Upgrade nitipan, or ask the user to run: nitipan setup git --uninstall <checkout>" >&2
		exit 1
	fi

	if [ "$status" -ne 0 ]; then
		exit "$status"
	fi
fi

if [ -x "$chained" ]; then
	printf '%s' "$input" | "$chained" "$@"
	exit $?
fi

exit 0
`

// prePushScript renders the pre-push hook. It always runs the binary. A push can delete remote refs that
// git does not list for the hook, such as --mirror does, so no input says that a push is harmless.
func prePushScript(executable, chained string) ([]byte, error) {
	if strings.ContainsRune(executable, '\n') {
		return nil, fmt.Errorf("nitipan path %q: want a path without a newline", executable)
	}

	text := strings.ReplaceAll(prePushTemplate, "@@NITIPAN@@", shellQuote(executable))
	text = strings.ReplaceAll(text, "@@CHAINED@@", chained)

	return []byte(text), nil
}

const prePushTemplate = `#!/bin/sh
# nitipan-managed hook
# Installed by nitipan setup git --install. Removed by nitipan setup git --uninstall, which also
# puts back the hook this one replaced. It refuses a push of a fenced branch that lacks the fence
# refs of nitipan push, then runs the hook it replaced, if any.

here=${0%/*}
nitipan=@@NITIPAN@@
chained=@@CHAINED@@

input=

while IFS=' ' read -r lref lsha rref rsha; do
	input="$input$lref $lsha $rref $rsha
"
done

if [ ! -x "$nitipan" ]; then
	nitipan=$(command -v nitipan 2>/dev/null) || nitipan=
fi

if [ -z "$nitipan" ]; then
	echo "nitipan: git refused the push, because the nitipan binary that checks leases is missing." >&2
	echo "Reinstall nitipan so it is on PATH, or ask the user to run: nitipan setup git --uninstall <checkout>" >&2
	exit 1
fi

printf '%s' "$input" | "$nitipan" hook pre-push "$@"
status=$?

if [ "$status" -eq 2 ]; then
	echo "nitipan: git refused the push, because $nitipan is too old for the hook installed in this clone." >&2
	echo "Upgrade nitipan, or ask the user to run: nitipan setup git --uninstall <checkout>" >&2
	exit 1
fi

if [ "$status" -ne 0 ]; then
	exit "$status"
fi

if [ -x "$chained" ]; then
	printf '%s' "$input" | "$chained" "$@"
	exit $?
fi

exit 0
`
