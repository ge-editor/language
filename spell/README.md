# language/spell — spell checker (libhunspell via cgo)

`libhunspell` を cgo で直接呼び出すスペルチェックのエンジンです。サードパーティの Go ラッパーは使いません。
`init()` で `editorleaf/spell` にエンジンを登録します。エディタ側（`editorleaf/spell`）は cgo を必要としません。

## ビルド

tree-sitter と同じく、既定のビルドには含まれません（C コンパイラも libhunspell も不要）。

```
# Hunspell の開発用ファイルが必要（pkg-config の hunspell.pc）
#   Debian/Ubuntu: apt install libhunspell-dev
#   Fedora:        dnf install hunspell-devel
#   Homebrew:      brew install hunspell

go build -tags hunspell .            # ge/ ディレクトリで
go build -tags "cts hunspell" .      # tree-sitter と併用
```

タグなしのビルドで `ESC x spell` を実行すると、「ビルドに含まれていない」と表示されます。

## 辞書

言語は当面 `en_US` だけです。Hunspell の `en_US.aff` と `en_US.dic`（UTF-8）を、次の順に探します。

1. `$GE_SPELL_DICT_DIR`
2. `<ユーザー設定ディレクトリ>/ge/spell/`（Linux は `~/.config/ge/spell/`）
3. システム（`/usr/share/hunspell`、`/usr/share/myspell`、`/opt/homebrew/share/hunspell`、`/Library/Spelling` など）
4. ビルドに同梱したもの（`language/spell/dict/` に置いてからビルドする）

同梱分は、libhunspell がファイルしか開けないため、`<ユーザーキャッシュ>/ge/spell/<内容のハッシュ>/` に書き出してから使います。
同梱する辞書ファイルのライセンスは、コピーする前に確認してください（`dict/README.md`）。

UTF-8 以外の辞書は読み込みを拒否します。

## モード別の辞書

言語モードの名前（小文字）を付けた辞書があれば、言語の辞書に加えて読み込みます。Go モードなら次のファイルです。

```
~/.config/ge/spell/go.dic    # 必須: 語のリスト
~/.config/ge/spell/go.aff    # 任意: 接辞ファイル。なければ言語（en_US）の .aff を使う
```

- 探す場所は `$GE_SPELL_DICT_DIR` と `<ユーザー設定ディレクトリ>/ge/spell/` です。モード名は `Go`、`Markdown`、`Org`、`Fundamental`（小文字にして `go.dic`、`markdown.dic` ...）です。
- `go.dic` は Hunspell の `.dic` 形式です。**1 行目が語数**（おおよそでよい）で、2 行目から 1 行 1 語です。1 行目が語数でないと、最初の語が失われます。
- 読み込むのは `ESC x spell`（または起動時の自動オン）で検査を始めるときです。コメントと文字列でも、識別子でも、この辞書の語は正しいものとして扱います。
- 辞書ごとに別の Hunspell インスタンスで調べるので、`go.aff` が UTF-8 でない、`go.dic` が壊れている場合は、その辞書だけを読み込まずに、`Spell dictionary: go: ...` と表示します（検査は続きます）。
- 候補（`spell-correct`）は言語の辞書だけから作ります。
- 読み込んだ辞書は `ESC x spell` の表示に出ます（例: `Spell check: on (+go), identifiers too`）。
- モードを切り替えても読み直しません。`spell` をオフにしてオンにし直してください。

`vocab.go` の組み込みの語彙（`strconv`、`goroutine` など）は、この辞書とは別に、識別子の検査で常に使われます。

## 初期値（起動時にオンにするか）

どちらも既定はオフです。変更する場所は `editorleaf/spell/defaults.go` の 2 つの変数です。

```go
// 検査を最初からオンにする言語モード（小文字の名前。"*" は他のすべて）
var OnByDefault = map[string]bool{
	"markdown":    true,
	"fundamental": true,
	"org":         true,
}

// 検査を始めたときに識別子も検査する言語モード
var IdentifiersByDefault = map[string]bool{"go": true}
```

- 名前で指定したモードが `"*"` より優先されます（`{"*": true, "go": false}` は Go 以外の全部）。
- 自動オンは、バッファを最初に表示するときの 1 回だけです。`ESC x spell` でオフにしたバッファは、別のペインに表示しても勝手にオンに戻りません。
- `ESC x spell-identifiers` は、検査がオフのときは、`IdentifiersByDefault` に関係なく、検査をオンにして識別子もオンにします。
- ge を `-tags hunspell` なしでビルドした場合や、辞書が見つからない場合は、黙ってオフのままです（`ESC x spell` で理由が表示されます）。
- 設定ファイル（`~/.config/ge/config.json`）はまだ読み込みません。

## 個人辞書

`ESC x spell-add` で追加した単語は `<ユーザー設定ディレクトリ>/ge/spell/personal_en_US.txt` に 1 行 1 語で追記され、次回から読み込まれます（空行と `#` で始まる行は無視）。
`ESC x spell-ignore` はその ge を終了するまで有効です。

## 検査の対象

`tokenize.go` の規則です。

- 検査しない: URL、メールアドレス、パス（`/` を含む語。`and/or` も対象外になります）、数字や `_` を含む語、ラテン文字以外が混じる語（日本語に続いた英字を含む）、3 文字未満の語、全部大文字の語（略語）
- camelCase は分割して検査します（`HTTPServer` → `HTTP`（略語なので無視）と `Server`）
- ハイフンは単語の区切りです
- `’`（U+2019）は `'` に直して照合します

どの範囲を検査するかは、言語モードごとの `Scope`（`editorleaf/spell/scope.go` の `ForMode`）が決めます。

| モード | 検査する範囲 |
|---|---|
| コードの mode（区切りを持つもの。現在は Go） | コメントと文字列だけ。コードは検査しない |
| Markdown | 本文（コードブロック、インラインコード、リンク先、HTML を除く） |
| Fundamental、Org、区切りを持たない mode | 全文 |

コードの mode は、tree-sitter を使わず、言語の区切り文字（行コメント、ブロックコメント、文字列）から字句的に判定します。そのため tree-sitter のビルド（`cts`）がなくても動きます。
文字列の中のエスケープ（`\n` の `n` など）と、文字リテラル（`'"'`）は検査しません。ヒアドキュメントやネストしたブロックコメントのように区切りだけでは判定できないものは、誤検出または見落としになります。

### 識別子（変数名・関数名・型名）の検査

既定では、コードの mode は**コメントと文字列だけ**を検査します。`ESC x spell-identifiers` で、コード部分の識別子も検査対象に加えたり外したりできます（バッファ単位のトグル。`spell` がオフなら一緒にオンにします）。区切りを持たない mode（text など）は全文を検査済みなので、このコマンドは意味がありません。

識別子の検査は、誤検出を抑えるため、本文とは別の規則を使います（`tokenize.go` の `codeTokens`）。

- `_`、数字、大文字の境目で分割します（`user_name`、`user2name`、`userName` はどれも `user` と `name`）。
- 4 文字未満の語は検査しません（`ctx`、`err`、`buf`、`tmp`、`idx` など）。
- 全部大文字の語（略語）、16 進数（`0xDEADBEEF`）、日本語などラテン文字以外が混じる識別子は検査しません。
- プログラマが知っている語は検査しません（`vocab.go` の `programmingTerms`。`strconv`、`Atoi`、`Sprintf`、`goroutine`、`args` など）。足りない語は `ESC x spell-add` で個人辞書に追加できます。

宣言と参照を区別しないので、ライブラリの名前や略語が誤検出されることがあります。宣言だけを検査するには、tree-sitter の構文木が必要です（未対応）。

### 新しい言語モードに対応する

モードが `lang.SpellDelimiterProvider`（`SpellDelimiters() *lang.Delimiters`）を実装するだけです。Go の例（`language/mode/go.go`）:

```go
var goDelimiters = &lang.Delimiters{
	LineComments:  []string{"//"},
	BlockComments: []lang.BlockDelimiter{{Open: "/*", Close: "*/"}},
	Strings: []lang.StringDelimiter{
		{Open: `"`, Close: `"`, Escape: '\\'},
		{Open: "`", Close: "`", Multiline: true},
		{Open: `'`, Close: `'`, Escape: '\\', Code: true}, // 文字リテラル: 検査しない
	},
}

func (gm *GoMode) SpellDelimiters() *lang.Delimiters { return goDelimiters }
```

実装しないモードは「全文が本文」として扱われます。

## 構成

| ファイル | 役割 |
|---|---|
| `hunspell.go` | libhunspell の薄いラッパー（`//go:build cgo`）。排他制御つき |
| `engine.go` | `editorleaf/spell.Engine` の実装（キャッシュ、個人辞書）と登録 |
| `dict.go` | 辞書の探索、同梱辞書の書き出し |
| `tokenize.go` | 単語の切り出し |

## テスト

```
go test ./language/spell/...
```

cgo が有効なとき（既定）は、`hunspell.go` と `engine.go` もビルドされるため、libhunspell の開発用ファイルが必要です。
無くても、トークナイザと辞書探索のテストだけなら次の方法で実行できます。

```
CGO_ENABLED=0 go test ./language/spell/...
```
