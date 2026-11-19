import {useEffect, useMemo, useRef, useState} from 'react';
import {api, errorText, QueryInfo, QueryResult} from '../api';
import {useResize} from '../useResize';
import {fmtDuration} from './LatencyChart';
import {qualifiedName, SchemaHelper} from './SchemaHelper';
import {SqlEditor, SqlEditorHandle} from './SqlEditor';

const IS_MAC = /Mac|iPhone|iPad/.test(navigator.userAgent);
const RUN_KEYS = IS_MAC ? '⌘↵' : 'Ctrl+Enter';
const NUMBER = /^-?\d+(\.\d+)?([eE][+-]?\d+)?$/;

/** useElapsed counts the whole seconds since `active` became true, so a long run shows it is alive. */
function useElapsed(active: boolean): number {
  const [secs, setSecs] = useState(0);
  useEffect(() => {
    if (!active) return;
    const start = Date.now();
    setSecs(0);
    const id = setInterval(() => setSecs(Math.floor((Date.now() - start) / 1000)), 1000);
    return () => clearInterval(id);
  }, [active]);
  return secs;
}

/** The query the editor starts with: the first Tableflow table when there is one. */
function starterQuery(info: QueryInfo): string {
  const table = info.tables?.[0];
  if (!info.catalog || !info.namespace || !table) return 'SELECT version();\n';
  return `SELECT *\nFROM ${qualifiedName(info, table)}\nLIMIT 10;\n`;
}

const SCHEMA_OPEN_KEY = 'pims.schema.open';

/** useSchemaOpen remembers whether the schema helper is showing; it starts open. */
function useSchemaOpen(): [boolean, (open: boolean) => void] {
  const [open, setOpen] = useState(() => {
    try {
      return localStorage.getItem(SCHEMA_OPEN_KEY) !== 'false';
    } catch {
      return true;
    }
  });
  return [open, (o) => {
    setOpen(o);
    try {
      localStorage.setItem(SCHEMA_OPEN_KEY, String(o));
    } catch {
      // A blocked store just forgets the choice.
    }
  }];
}

export function QueryPanel({info, ready}: {info: QueryInfo; ready: boolean}) {
  const starter = useMemo(() => starterQuery(info), [info.catalog, info.namespace, info.tables?.[0]]);
  const [text, setText] = useState(starter);
  const [selected, setSelected] = useState('');
  // Once the operator has typed, the starter query no longer replaces their text.
  const [edited, setEdited] = useState(false);
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<QueryResult | null>(null);
  const [error, setError] = useState('');
  const [schemaOpen, setSchemaOpen] = useSchemaOpen();
  const elapsed = useElapsed(running);
  const editor = useRef<SqlEditorHandle>(null);
  const main = useRef<HTMLDivElement>(null);
  // The editor takes the height the operator drags it to; the results get the rest.
  const split = useResize({
    axis: 'y',
    initial: 200,
    min: 80,
    max: () => Math.max(80, (main.current?.clientHeight ?? 600) - 120),
    storageKey: 'pims.query.editor',
  });

  useEffect(() => {
    if (!edited) setText(starter);
  }, [starter, edited]);

  // The selection is what runs when there is one, as in most SQL editors.
  const toRun = selected.trim() ? selected : text;
  const canRun = ready && !running && toRun.trim() !== '';

  async function run() {
    if (!canRun) return;
    setRunning(true);
    setError('');
    try {
      setResult(await api.runQuery(toRun));
    } catch (err) {
      setResult(null);
      setError(errorText(err));
    } finally {
      setRunning(false);
    }
  }

  const tables = info.tables ?? [];
  return (
    <section className="query" aria-labelledby="query-title">
      <header className="query-head">
        <h2 id="query-title">SQL</h2>
        <span className="fine query-note">
          {ready ? 'Runs in the DuckDB container, in a fresh session each time.' : 'Set up the demo to run queries.'}
        </span>
        <div className="head-actions">
          <span className="fine query-keys">{RUN_KEYS} to run</span>
          <button aria-pressed={schemaOpen} aria-controls="schema-panel" onClick={() => setSchemaOpen(!schemaOpen)}>
            Tables
          </button>
          <button className="primary" disabled={!canRun} onClick={run} title={ready ? undefined : 'Set up the demo first'}>
            {running ? `Running… ${elapsed}s` : selected.trim() ? 'Run selection' : 'Run'}
          </button>
        </div>
      </header>

      <div className="query-body">
        <div className="query-main" ref={main}>
          <div className="query-editor" style={{height: split.size}}>
            <SqlEditor
              handle={editor}
              value={text}
              onChange={(t) => { setText(t); setEdited(true); }}
              onSelect={setSelected}
              onRun={run}
              placeholderText="Write a DuckDB query"
            />
          </div>
          <div
            className={split.dragging ? 'resizer resizer-h is-dragging' : 'resizer resizer-h'}
            aria-label="Resize the editor"
            {...split.handleProps}
          />

          <div className="query-output">
            {error && <p className="error query-error" role="alert">{error}</p>}
            {result && <QueryResults result={result}/>}
            {!error && !result && (
              <p className="fine query-empty">
                {info.catalog && info.namespace
                  ? <>The Tableflow tables are attached as <code>{info.catalog}."{info.namespace}".&lt;table&gt;</code>
                    {tables.length > 0 && <>: {tables.map((t, i) => <span key={t}>{i > 0 && ', '}<code>{t}</code></span>)}</>}.
                    {' '}Try <code>DESCRIBE</code> to see a table's columns.</>
                  : <>Tableflow tables appear here once Tableflow is enabled.</>}
              </p>
            )}
          </div>
        </div>

        {schemaOpen && (
          <div id="schema-panel" className="query-schema">
            <SchemaHelper info={info} onInsert={(t) => editor.current?.insert(t)}/>
          </div>
        )}
      </div>
    </section>
  );
}

function QueryResults({result}: {result: QueryResult}) {
  const {columns, rows, totalRows, elapsedMs} = result;
  // A column is numeric, and right-aligned, when every value in it is.
  const numeric = useMemo(
    () => columns.map((_, c) => rows.some((r) => r[c] !== null) && rows.every((r) => r[c] === null || NUMBER.test(r[c]!))),
    [columns, rows],
  );
  const shown = rows.length;
  return (
    <div className="query-result">
      <p className="fine query-status" role="status">
        {totalRows === 0
          ? 'No rows returned'
          : `${shown === totalRows ? totalRows.toLocaleString() : `First ${shown.toLocaleString()} of ${totalRows.toLocaleString()}`} ${totalRows === 1 ? 'row' : 'rows'}`}
        {' · '}{fmtDuration(elapsedMs)}
        {shown < totalRows && ' · Add a LIMIT or a WHERE to see the rest'}
      </p>
      {columns.length > 0 && (
        <div className="table-wrap query-table" tabIndex={0} role="region" aria-label="Query results">
          <table className="data">
            <thead>
              <tr>{columns.map((c, i) => <th key={i} className={numeric[i] ? 'num' : undefined}>{c}</th>)}</tr>
            </thead>
            <tbody>
              {rows.map((row, r) => (
                <tr key={r}>
                  {row.map((cell, c) => cell === null
                    ? <td key={c} className={numeric[c] ? 'null num' : 'null'}>NULL</td>
                    : <td key={c} className={numeric[c] ? 'num' : undefined} title={cell.length > 40 ? cell : undefined}>{cell}</td>)}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
