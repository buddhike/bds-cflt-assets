import {useState} from 'react';
import {QueryInfo} from '../api';
import {ColumnDoc, TABLE_DOCS, TableDoc} from '../tableDocs';

/** qualifiedName is how the query window names a table: the attached catalog's, once there is one. */
export function qualifiedName(info: QueryInfo, table: string): string {
  return info.catalog && info.namespace ? `${info.catalog}."${info.namespace}".${table}` : table;
}

interface Props {
  info: QueryInfo;
  /** Puts text at the editor's cursor. */
  onInsert: (text: string) => void;
}

/** SchemaHelper lists each table with its columns, and puts the names the operator clicks into the query. */
export function SchemaHelper({info, onInsert}: Props) {
  // Names the setup has not enabled yet are still worth reading about, so every documented table is listed.
  const [open, setOpen] = useState<Set<string>>(() => new Set([TABLE_DOCS[0].name]));
  const toggle = (name: string) => setOpen((o) => {
    const next = new Set(o);
    if (!next.delete(name)) next.add(name);
    return next;
  });

  return (
    <aside className="schema" aria-label="Table schemas">
      <header className="schema-head">
        <h3>Tables</h3>
        <span className="fine">Click a name to put it in the query.</span>
      </header>
      <div className="schema-list">
        {TABLE_DOCS.map((t) => (
          <TableEntry
            key={t.name}
            table={t}
            qualified={qualifiedName(info, t.name)}
            open={open.has(t.name)}
            onToggle={() => toggle(t.name)}
            onInsert={onInsert}
          />
        ))}
      </div>
      <p className="fine schema-foot">
        Columns and types are those of the MySQL tables. Run <code>DESCRIBE</code> on a table for the Iceberg table's own.
      </p>
    </aside>
  );
}

function TableEntry({table, qualified, open, onToggle, onInsert}: {
  table: TableDoc;
  qualified: string;
  open: boolean;
  onToggle: () => void;
  onInsert: (text: string) => void;
}) {
  const bodyId = `schema-${table.name}`;
  return (
    <section className="schema-table">
      <div className="schema-table-head">
        <button className="schema-toggle" aria-expanded={open} aria-controls={bodyId} onClick={onToggle}>
          <svg className={open ? 'chevron chevron-open' : 'chevron'} viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
            <path d="M4 2l4 4-4 4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"/>
          </svg>
          <code className="schema-name">{table.name}</code>
          <span className="fine">{table.columns.length} columns</span>
        </button>
        <button className="schema-insert" onClick={() => onInsert(qualified)} title={`Insert ${qualified}`} aria-label={`Insert ${qualified} into the query`}>
          Insert
        </button>
      </div>
      {open && (
        <div id={bodyId} className="schema-body">
          <p className="fine schema-summary">{table.summary}</p>
          <ul className="schema-cols">
            {table.columns.map((c) => <Column key={c.name} column={c} onInsert={onInsert}/>)}
          </ul>
        </div>
      )}
    </section>
  );
}

function Column({column: c, onInsert}: {column: ColumnDoc; onInsert: (text: string) => void}) {
  const tags = [
    c.primaryKey && 'PK',
    c.unique && 'unique',
    c.references && `→ ${c.references}`,
    c.nullable && 'null',
  ].filter(Boolean) as string[];
  const detail = [c.values && `One of ${c.values.join(', ')}`, c.note].filter(Boolean).join('. ');
  return (
    <li>
      <button className="schema-col" onClick={() => onInsert(c.name)} title={`Insert ${c.name}`}>
        <span className="schema-col-main">
          <code className="schema-col-name">{c.name}</code>
          {tags.map((t) => <span key={t} className="schema-tag">{t}</span>)}
        </span>
        <code className="schema-col-type">{c.type}</code>
        {detail && <span className="schema-col-note">{detail}</span>}
      </button>
    </li>
  );
}
