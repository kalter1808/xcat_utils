# xcat-ports: standalone pping и xdsh

Независимые порты утилит xCAT `/opt/xcat/bin/pping` и `/opt/xcat/bin/xdsh`
на Go. Не требуют xcatd, xCAT-базы и Perl: noderange разворачивается локально,
хосты разрешаются через DNS/hosts на этапе ssh/fping/nmap.

## Сборка (nix)

```sh
nix build .#pping .#xdsh   # или .#default — оба бинарника сразу
nix shell .#pping .#xdsh
nix develop                # dev-shell с go, fping, nmap, openssh
```

Обычная сборка Go тоже работает: `go build ./cmd/pping ./cmd/xdsh`.

## pping

Семантика оригинала (xCAT-client/bin/pping):

- `pping noderange` — по умолчанию через **nmap** (`-PE --system-dns --send-ip
  -sP --unprivileged -PA80,443,22`), если nmap есть в системе; иначе/с `-f` —
  через **fping**
- вывод: `node: ping` / `node: noping` (независимые узлы — в порядке
  обнаружения, недоступные — отсортированы в конце, как в оригинале)
- `-i|--interface eth0,ib0` — последовательный опрос интерфейсных суффиксов
  (суффикс `-hf<N>` срезается)
- `-f|--use_fping`, `-X|--noexpand` (запятнанный список уже развёрнут),
  `-h`, `-v`

## xdsh

Семантика bypass-режима оригинала (DSHCLI.pm / DSHCore.pm / SSH.pm), порт
всегда работает без демона:

- целевые узлы: `ssh [-o BatchMode=yes] [-x] [user@]node '<command>'`
  (BatchMode/-x добавляются для OpenSSH, как в SSH.pm)
- команда на узле: `export NODE=<node>; <pre-command><command>;
  export DSH_TARGET_RC=$?; echo ":DSH_TARGET_RC=${DSH_TARGET_RC}:"` —
  строка-маркер вырезается из вывода и даёт код возврата удалённой команды
- вывод: каждая строка с префиксом `node: `; по умолчанию буферизация по
  узлу (сначала stdout, потом stderr узла), `-s` — потоковый режим
- exit-код = число неудавшихся узлов (ssh-код, удалённый код или
  отсутствие маркера)
- fanout по умолчанию **64** (`-f` / `DSH_FANOUT`)
- `-t N` / `DSH_TIMEOUT` — таймаут в секундах (по умолчанию **5** секунд, `0` — без таймаута):
  SIGINT всем активным детям, сообщение как в оригинале; ожидающие узлы продолжают запускаться
  (у оригинала `last` закомментирован)
- `-l user` / `DSH_TO_USERID`; `-r shell` / `DSH_NODE_RSH`;
  `-o opts` / `DSH_NODE_OPTS` (проверка: путь существует, исполняем,
  не rsync)
- `-k` / `--ignore-host-key` / `XDSH_IGNORE_HOST_KEY=1` (`DSH_IGNORE_HOST_KEY=1`) —
  игнорирование проверки host key (`StrictHostKeyChecking=no`,
  `UserKnownHostsFile=/dev/null`, `GlobalKnownHostsFile=/dev/null`,
  `LogLevel=ERROR` для OpenSSH/SCP)
- `-z` — строка `Remote_command_rc = N`; `-Q` — тишина; `--nodestatus` —
  `Remote_command_successful/failed, error_code=N`; `-m` — прогресс
  `dsh> ...`; `-e script` — scp скрипта в /tmp и запуск; `--sudo`;
  `-L` — без locale pre-command; `-B` принят для совместимости
- предупреждения о неподдерживаемых переменных (DSH_LIST, WCOLL, ...) —
  как в `check_invalid_exports`

## Noderange (полный синтаксис NodeRange.pm, без БД)

- `node1,node2` — списки; результат сортирован и уникален
- `node[1-100]`, `node[001-200]` (с сохранением нулей), `f[1-2]n[1-3]`
  (несколько групп скобок), `192.168.0.[1-20]`
- `rack1-rack4`, `node1:node200` — дефис/двоеточие: ровно одно числовое
  поле может отличаться; имена с дефисами поддержаны (правило нечётного
  числа дефисов)
- `node10+3` — инкремент; чистые числа `10-12` получают префикс `node`
  (`XCAT_NODE_PREFIX`/`XCAT_NODE_SUFFIX`)
- `,-node3`, `-(a,b)` — исключения применяются в конце и всегда сильнее
  включений; `@` — пересечение (`group1@group2`, `a@(b,c)`);
  `( ... )` — группировка; `^/path/file` — файл со списком (по токену
  до пробела/двоеточия, `#`/`^` — комментарии)
- `/regex/` без БД возвращается как есть (нечему матчиться)

## Тесты

```sh
go test ./...     # unit-тесты noderange и xdsh
bash smoke.sh     # smoke pping/xdsh (fake ssh для детерминированных проверок)
nix flake check
```

## Документация (man-страницы)

В проект включены man-страницы:
- `man/man1/pping.1` (`man -l man/man1/pping.1`)
- `man/man1/xdsh.1` (`man -l man/man1/xdsh.1`)

При установке через Nix (`nix shell`, `nix profile install`) man-страницы подключаются автоматически в `share/man/man1/`.

## Ограничения порта

- нет xcatd/базы: группы узлов из `nodelist`, dyn-группы, `site.excludenodes`
  не работают — узлы и диапазоны задаются прямо в noderange/DNS
- `-K` (раздача ключей), `-c`, `--devicetype`, `-i rootimg`, xdcp — вне
  рамок порта
