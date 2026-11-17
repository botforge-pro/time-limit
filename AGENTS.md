# time-limit

Before the first action read the whole wiki «Как мы пишем код»:
wikilayer://wikilayer.org/smee-again/codestyle

The process tests start this CLI and helper processes. Run them through Make;
`TIME_LIMIT` names an already checked supervisor used to bound the complete test
recipe. The binary being tested is built separately into `bin/time-limit`.
