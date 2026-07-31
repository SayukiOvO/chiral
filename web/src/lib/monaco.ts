// The editor core, and nothing else.
//
// `monaco-editor`'s package entry re-exports every language it ships —
// TypeScript, CSS, HTML and some eighty basic-language tokenizers — and each
// language service becomes its own worker chunk. The build carried 6.7 MB of
// ts.worker, 1.0 MB of css.worker, 699 kB of html.worker and a 3.8 MB editor
// chunk, none of it reachable from here: the only thing this editor ever opens
// is a template, in the Monarch language registered at the bottom of this
// file. Not even Monaco's JSON service is used — the tokenizer is ours,
// because a variable inside a string still has to be highlighted, and JSON's
// would swallow it.
import * as monaco from "monaco-editor/editor/editor.api";
import { loader } from "@monaco-editor/react";
import editorWorker from "monaco-editor/editor/editor.worker.js?worker";

/**
 * Monaco setup. Two things matter here:
 *
 * 1. `loader.config({ monaco })` — by default @monaco-editor/react pulls
 *    Monaco from a CDN at runtime, which fails on restricted networks. We
 *    hand it the copy Vite bundled instead, so the editor works offline.
 * 2. The worker is wired explicitly for the same reason.
 */
self.MonacoEnvironment = {
  getWorker() {
    return new editorWorker();
  },
};

loader.config({ monaco });

// Themes matching the app's black-grey palette, so the editor reads as part
// of the console rather than a window into someone else's IDE.
monaco.editor.defineTheme("chiral-dark", {
  base: "vs-dark",
  inherit: true,
  rules: [
    { token: "chiral-var", foreground: "3fb950", fontStyle: "bold" },
    { token: "string.value.json", foreground: "c9d1d9" },
    { token: "string.key.json", foreground: "9a9aa2" },
    { token: "number", foreground: "c9d1d9" },
    { token: "keyword.json", foreground: "c9d1d9" },
  ],
  colors: {
    "editor.background": "#141416",
    "editor.foreground": "#ededf0",
    "editorLineNumber.foreground": "#41414a",
    "editorLineNumber.activeForeground": "#9a9aa2",
    "editor.lineHighlightBackground": "#1a1a1d",
    "editorIndentGuide.background1": "#262629",
    "editorGutter.background": "#141416",
    "scrollbarSlider.background": "#34343a80",
  },
});

monaco.editor.defineTheme("chiral-light", {
  base: "vs",
  inherit: true,
  rules: [
    { token: "chiral-var", foreground: "1a7f37", fontStyle: "bold" },
    { token: "string.value.json", foreground: "18181b" },
    { token: "string.key.json", foreground: "6b6b73" },
  ],
  colors: {
    "editor.background": "#ffffff",
    "editor.foreground": "#18181b",
    "editorLineNumber.foreground": "#c2c2c9",
    "editorLineNumber.activeForeground": "#6b6b73",
    "editor.lineHighlightBackground": "#fafafa",
    "editorGutter.background": "#ffffff",
  },
});

/**
 * A JSON-with-{{variables}} language. Plain JSON mode would flag `{{name}}`
 * inside a string as fine but a bare `{{port}}` (an unquoted number slot) as a
 * syntax error, which is exactly the construct our templates use — so the
 * template language is its own thing, tokenized for highlighting only.
 */
const LANG_ID = "chiral-template";

let registered = false;

export function registerTemplateLanguage() {
  if (registered) return;
  registered = true;

  monaco.languages.register({ id: LANG_ID });
  monaco.languages.setMonarchTokensProvider(LANG_ID, {
    tokenizer: {
      root: [
        // A bare {{var}} standing in for a number or whole value.
        [/\{\{\s*[a-zA-Z0-9_.]+\s*\}\}/, "chiral-var"],
        // Keys are matched whole (they never carry variables) so they keep
        // their own colour.
        [/"(?:[^"\\]|\\.)*"(?=\s*:)/, "string.key.json"],
        // Values are entered as a state: a variable inside a string must still
        // be highlighted, and matching the whole string first would swallow it
        // — which is the common case, e.g. "{{sni}}:443".
        [/"/, { token: "string.value.json", next: "@stringValue" }],
        [/\b-?\d+(\.\d+)?\b/, "number"],
        [/\b(true|false|null)\b/, "keyword.json"],
        [/[{}[\],:]/, "delimiter"],
        [/\/\/.*$/, "comment"],
      ],
      stringValue: [
        [/\{\{\s*[a-zA-Z0-9_.]+\s*\}\}/, "chiral-var"],
        [/\\./, "string.escape"],
        [/"/, { token: "string.value.json", next: "@pop" }],
        [/[^"\\{]+/, "string.value.json"],
        [/\{/, "string.value.json"],
      ],
    },
  });
  monaco.languages.setLanguageConfiguration(LANG_ID, {
    brackets: [
      ["{", "}"],
      ["[", "]"],
    ],
    autoClosingPairs: [
      { open: "{", close: "}" },
      { open: "[", close: "]" },
      { open: '"', close: '"' },
    ],
  });
}

export { monaco, LANG_ID };
