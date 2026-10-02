# Generates the gif and screenshots for the readme. Needs loki with some history, which
# `earthly +dev` sets up; the tape starts a slow generator of its own for the live lines.
Output ./assets/cassette.gif

Require go

Set FontSize 16
Set Width 1600
Set Height 800
Set BorderRadius 10
Set TypingSpeed 80ms
# Every frame of a live tail differs, a lower rate keeps the gif small.
Set Framerate 12

Hide
  Type@10ms "go build -o build/seed ./seed && go build -o build/kontrolplane/stdout . && clear && { build/seed --backfill 0 --rate 3 >/dev/null 2>&1 & } && ./build/kontrolplane/stdout; kill %1"
  Enter
  Wait+Screen@60s /values of/
  Sleep 1s
Show

Sleep 3s

Screenshot ./assets/pages/labels.png
# VHS takes the shot with a frame of its own, which can come late on a busy machine.
Sleep 2s

# app=~"api|worker"
Tab
Sleep 1s
Space
Sleep 500ms
Down@400ms 3
Space
Sleep 1500ms

# namespace="prod", whose values are now those of the api and worker streams
Tab
Down@400ms 2
Sleep 1500ms
Tab
Space
Sleep 2s

Screenshot ./assets/pages/selection.png
Sleep 2s

Enter
Sleep 5s

Screenshot ./assets/pages/tail.png
Sleep 2s

Type "/"
Sleep 500ms
Type "error"
Sleep 1s
Enter
Sleep 3s

Screenshot ./assets/pages/filter.png
Sleep 2s

Space
Sleep 500ms
Enter
Sleep 4s

Screenshot ./assets/pages/line.png
Sleep 2s

Type "]"
Sleep 2s
Type "q"
Sleep 1s
Escape
Sleep 2s

Type "w"
Sleep 3s

Screenshot ./assets/pages/wrap.png
Sleep 2s

Type "w"
Sleep 1s
Type "e"
Sleep 500ms
Ctrl+U
Type '{app="worker"} |= "failed"'
Sleep 1s
Enter
Sleep 4s

Type "?"
Sleep 3s

Screenshot ./assets/pages/help.png
Sleep 2s

Type "?"
Sleep 1s

# Quitting also stops the generator, see the command it was started with.
Hide
  Ctrl+C
  Sleep 1s
