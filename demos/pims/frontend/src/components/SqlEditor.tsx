import {Ref, useEffect, useImperativeHandle, useRef} from 'react';
import {Annotation, EditorState, Transaction} from '@codemirror/state';
import {drawSelection, EditorView, highlightActiveLine, keymap, lineNumbers, placeholder} from '@codemirror/view';
import {defaultKeymap, history, historyKeymap} from '@codemirror/commands';
import {bracketMatching, HighlightStyle, syntaxHighlighting} from '@codemirror/language';
import {PostgreSQL, SQLDialect, sql} from '@codemirror/lang-sql';
import {tags} from '@lezer/highlight';

// DuckDB's SQL is close to PostgreSQL's. These are the words DuckDB adds
// that the PostgreSQL dialect does not already highlight.
const DuckDB = SQLDialect.define({
  ...PostgreSQL.spec,
  keywords: `${PostgreSQL.spec.keywords} summarize pivot unpivot qualify sample asof positional anti semi macro pragma install export use try_cast`,
  types: `${PostgreSQL.spec.types} hugeint uhugeint ubigint uinteger usmallint utinyint tinyint blob list struct map union bit varint timetz timestamp_s timestamp_ms timestamp_ns`,
});

// Colours are CSS variables (see style.css), so the editor follows the
// light and dark themes without being rebuilt.
const theme = EditorView.theme({
  '&': {
    color: 'var(--code-text)', backgroundColor: 'var(--code-bg)', fontSize: '13px',
    border: '1px solid var(--border)', borderRadius: 'var(--r-md)', height: '100%',
  },
  '&.cm-focused': {outline: '2px solid var(--accent)', outlineOffset: '-1px'},
  '.cm-scroller': {fontFamily: 'var(--mono)', lineHeight: '1.55', overflow: 'auto'},
  '.cm-content': {caretColor: 'var(--code-text)', padding: '8px 0'},
  '.cm-line': {padding: '0 12px'},
  '.cm-cursor': {borderLeftColor: 'var(--code-text)'},
  '.cm-gutters': {backgroundColor: 'transparent', color: 'var(--code-muted)', border: 'none'},
  '.cm-lineNumbers .cm-gutterElement': {padding: '0 4px 0 12px', minWidth: '28px'},
  // Only the focused editor has a current line to point at.
  '&.cm-focused .cm-activeLine': {backgroundColor: 'color-mix(in srgb, var(--accent) 6%, transparent)'},
  '.cm-activeLine': {backgroundColor: 'transparent'},
  '.cm-activeLineGutter': {backgroundColor: 'transparent', color: 'var(--code-text)'},
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
    backgroundColor: 'color-mix(in srgb, var(--accent) 28%, transparent)',
  },
  '.cm-matchingBracket': {backgroundColor: 'color-mix(in srgb, var(--accent) 22%, transparent)', outline: 'none'},
  '.cm-placeholder': {color: 'var(--code-muted)'},
});

const highlight = HighlightStyle.define([
  {tag: tags.keyword, color: 'var(--code-keyword)', fontWeight: '600'},
  {tag: tags.typeName, color: 'var(--code-type)'},
  {tag: tags.standard(tags.name), color: 'var(--code-builtin)'},
  {tag: [tags.string, tags.special(tags.name)], color: 'var(--code-string)'},
  {tag: tags.special(tags.string), color: 'var(--code-ident)'},
  {tag: [tags.number, tags.bool, tags.null], color: 'var(--code-number)'},
  {tag: [tags.lineComment, tags.blockComment], color: 'var(--code-muted)', fontStyle: 'italic'},
  {tag: [tags.operator, tags.punctuation, tags.paren, tags.squareBracket, tags.brace], color: 'var(--code-muted)'},
]);

// Marks edits the parent made, which it already knows about.
const external = Annotation.define<boolean>();

/** What a parent can ask of the editor. */
export interface SqlEditorHandle {
  /** Replaces the selection with `text` (so it lands at the cursor when nothing is selected) and focuses the editor. */
  insert: (text: string) => void;
}

export interface SqlEditorProps {
  value: string;
  /** Called when the operator edits the text, not when `value` is replaced from outside. */
  onChange: (text: string) => void;
  /** Called with the selected text, or '' when nothing is selected, whenever either changes. */
  onSelect: (selected: string) => void;
  /** Called for Cmd/Ctrl+Enter. */
  onRun: () => void;
  placeholderText?: string;
  handle?: Ref<SqlEditorHandle>;
}

/** SqlEditor is a CodeMirror editor with DuckDB SQL highlighting. */
export function SqlEditor({value, onChange, onSelect, onRun, placeholderText, handle}: SqlEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  useImperativeHandle(handle, () => ({
    insert(text) {
      const v = view.current;
      if (!v) return;
      const {from, to} = v.state.selection.main;
      const before = v.state.sliceDoc(Math.max(0, from - 1), from);
      const after = v.state.sliceDoc(to, to + 1);
      // Keep the name apart from a word it lands against, so two inserts in a row do not run together.
      const pre = /[\w"')]/.test(before) ? ' ' : '';
      const post = /[\w"(]/.test(after) ? ' ' : '';
      // This is the operator's edit, so it reaches onChange and can be undone.
      v.dispatch({
        changes: {from, to, insert: pre + text + post},
        selection: {anchor: from + pre.length + text.length},
        scrollIntoView: true,
        userEvent: 'input',
      });
      v.focus();
    },
  }), []);
  // The editor is built once, so it calls the latest callbacks through this.
  const callbacks = useRef({onChange, onSelect, onRun});
  useEffect(() => { callbacks.current = {onChange, onSelect, onRun}; });

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          history(),
          drawSelection(),
          highlightActiveLine(),
          bracketMatching(),
          sql({dialect: DuckDB}),
          syntaxHighlighting(highlight),
          theme,
          EditorView.contentAttributes.of({'aria-label': 'SQL query', spellcheck: 'false'}),
          ...(placeholderText ? [placeholder(placeholderText)] : []),
          keymap.of([
            {key: 'Mod-Enter', preventDefault: true, run: () => { callbacks.current.onRun(); return true; }},
            ...defaultKeymap,
            ...historyKeymap,
          ]),
          EditorView.updateListener.of((u) => {
            if (u.docChanged && !u.transactions.some((t) => t.annotation(external))) {
              callbacks.current.onChange(u.state.doc.toString());
            }
            if (u.docChanged || u.selectionSet) {
              const {from, to} = u.state.selection.main;
              callbacks.current.onSelect(u.state.sliceDoc(from, to));
            }
          }),
        ],
      }),
    });
    view.current = v;
    return () => { v.destroy(); view.current = null; };
    // Built once; later `value`s arrive through the effect below.
  }, []);

  // Follow the parent when it replaces the text, for example with a new
  // starter query. That is not an edit the operator could want to undo.
  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({changes: {from: 0, to: v.state.doc.length, insert: value}, annotations: [external.of(true), Transaction.addToHistory.of(false)]});
    }
  }, [value]);

  return <div ref={host} className="sql-editor"/>;
}
