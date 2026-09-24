#!/bin/sh
# PRISM environment check: what is installed, what is optional and what it unlocks.
ok=0; warn=0
good() { printf '  \033[32m✔\033[0m %-12s %s\n' "$1" "$2"; }
miss() { printf '  \033[31m✘\033[0m %-12s %s\n' "$1" "$2"; ok=1; }
opt()  { printf '  \033[33m○\033[0m %-12s %s\n' "$1" "$2"; warn=1; }
have() { command -v "$1" >/dev/null 2>&1; }

echo "Required"
have go   && good go   "$(go version | awk '{print $3}')"             || miss go   "install Go (brew install go)"
have node && good node "$(node --version)"                            || miss node "install Node.js (brew install node) — builds the web UI"
have npm  && good npm  "$(npm --version)"                             || miss npm  "comes with Node.js"

echo "Optional (features degrade gracefully without them)"
if have aria2c; then good aria2c "magnet / torrent / FTP downloads"
else opt aria2c "missing — magnet, torrent and FTP downloads fail.  brew install aria2"; fi

chrome=""
for p in "/Applications/Google Chrome.app" "/Applications/Chromium.app" "/Applications/Brave Browser.app" "/Applications/Microsoft Edge.app"; do
  [ -d "$p" ] && chrome="$p" && break
done
[ -z "$chrome" ] && for b in google-chrome chromium chromium-browser chrome; do have "$b" && chrome="$b" && break; done
if [ -n "$chrome" ]; then good chrome "$chrome"
else opt chrome "missing — JS-heavy pages and the browser_* tools are unavailable.  brew install --cask google-chrome"; fi

if have pg_isready; then good pg_isready "client tools present"
else opt pg_isready "missing — only needed to check a local server by hand (brew install postgresql@16)"; fi

echo
echo "PostgreSQL (with pgvector) is configured in the web UI at first start; without pgvector, memory search uses a slower fallback."
if [ "$ok" -ne 0 ]; then echo "Some required tools are missing."; exit 1; fi
[ "$warn" -ne 0 ] && echo "Everything required is here. Run 'make deps' to install the optional tools too."
exit 0
