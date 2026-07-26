import { useEffect, useRef } from "react";
import Editor, { type OnMount } from "@monaco-editor/react";
import { LANG_ID, monaco, registerTemplateLanguage } from "../lib/monaco";
import type { VarProblem } from "../lib/template";

registerTemplateLanguage();

/**
 * The Monaco half of TemplateEditor, split into its own module so it can be
 * lazy-loaded: Monaco is ~4 MB and the node dashboard never shows an editor.
 */
export default function MonacoEditor({
  value,
  onChange,
  problems,
  height,
  dark,
}: {
  value: string;
  onChange: (v: string) => void;
  problems: VarProblem[];
  height: number;
  dark: boolean;
}) {
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);

  // Underline every bad reference inline, the way a type error would be shown
  // — the operator should not have to hit save to learn a variable is missing.
  useEffect(() => {
    const ed = editorRef.current;
    const model = ed?.getModel();
    if (!model) return;
    const markers: monaco.editor.IMarkerData[] = [];
    for (const p of problems) {
      const matches = model.findMatches(
        `\\{\\{\\s*${p.name.replace(/\./g, "\\.")}\\s*\\}\\}`,
        true,
        true,
        false,
        null,
        false,
      );
      for (const m of matches) {
        markers.push({
          severity: monaco.MarkerSeverity.Error,
          message: `${p.name} — ${p.reason}`,
          startLineNumber: m.range.startLineNumber,
          startColumn: m.range.startColumn,
          endLineNumber: m.range.endLineNumber,
          endColumn: m.range.endColumn,
        });
      }
    }
    monaco.editor.setModelMarkers(model, "chiral", markers);
  }, [problems, value]);

  const onMount: OnMount = (editor) => {
    editorRef.current = editor;
  };

  return (
    <Editor
      height={height}
      language={LANG_ID}
      theme={dark ? "chiral-dark" : "chiral-light"}
      value={value}
      onChange={(v) => onChange(v ?? "")}
      onMount={onMount}
      options={{
        fontSize: 12.5,
        fontFamily: "'Space Mono', ui-monospace, monospace",
        minimap: { enabled: false },
        scrollBeyondLastLine: false,
        lineNumbersMinChars: 3,
        renderLineHighlight: "line",
        padding: { top: 12, bottom: 12 },
        tabSize: 2,
        automaticLayout: true,
        scrollbar: { verticalScrollbarSize: 8, horizontalScrollbarSize: 8 },
        overviewRulerLanes: 0,
      }}
    />
  );
}
