# vhs tape for assets/readme/run.gif: the CLI against the demo instance scripts/demo/seed.sh
# leaves running. Record with `vhs assets/readme/run.tape` after `scripts/demo/seed.sh up`.
Output assets/readme/run.gif
Set Shell bash
Set FontSize 14
Set Width 880
Set Height 520
Set Padding 16
Set Theme { "background": "#14181A", "foreground": "#E6E8EA", "cursor": "#E6E8EA", "black": "#14181A", "red": "#E5484D", "green": "#30A46C", "yellow": "#F5A524", "blue": "#5B9BF5", "magenta": "#B57BEE", "cyan": "#2EC4B6", "white": "#E6E8EA", "brightBlack": "#6B7280", "brightRed": "#E5484D", "brightGreen": "#30A46C", "brightYellow": "#F5A524", "brightBlue": "#5B9BF5", "brightMagenta": "#B57BEE", "brightCyan": "#2EC4B6", "brightWhite": "#FFFFFF" }
Set TypingSpeed 35ms
Hide
Type "source /tmp/vink-demo/env && cd /tmp/vink-demo && export PATH=$PWD:$PATH && clear" Enter
Show
Sleep 500ms
Type "vink ls" Enter
Sleep 3s
Type "vink run nightly-backup -- ./backup.sh" Enter
Sleep 4s
Type "vink ls" Enter
Sleep 3s
Type "vink logs nightly-backup -n 3" Enter
Sleep 5s
