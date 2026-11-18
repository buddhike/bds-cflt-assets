// Node and edge geometry is in the SVG's own 1000-unit coordinate space; the
// SVG scales to the card's width.
const NODE_W = 128;
const NODE_H = 64;
const STEP = 162; // column pitch: NODE_W plus a 34-unit gap for the arrow
const X0 = 31;
const ROW1 = 96;
const ROW2 = 206;

const col = (i: number) => X0 + STEP * i;
const cx = (i: number) => col(i) + NODE_W / 2;

interface NodeSpec {
  col: number;
  row: number;
  title: string;
  lines: [string, string];
}

const NODES: NodeSpec[] = [
  {col: 0, row: ROW1, title: 'Simulator', lines: ['PIMS app', 'clinic events']},
  {col: 1, row: ROW1, title: 'MySQL', lines: ['patients · visits', 'appointments']},
  {col: 2, row: ROW1, title: 'Debezium', lines: ['Kafka Connect', 'reads the binlog']},
  {col: 3, row: ROW1, title: 'Kafka topics', lines: ['Avro · compacted', 'Schema Registry']},
  {col: 4, row: ROW1, title: 'Tableflow', lines: ['materialises topics', 'as Iceberg']},
  {col: 5, row: ROW1, title: 'Iceberg tables', lines: ['managed storage', 'snapshot per commit']},
  {col: 0, row: ROW2, title: 'Latency chart', lines: ['PIMS app', 'plots each snapshot']},
  {col: 1, row: ROW2, title: 'DuckDB', lines: ['CLI in Docker', 'compares snapshots']},
];

const MID1 = ROW1 + NODE_H / 2;
const MID2 = ROW2 + NODE_H / 2;

// The write path, left to right.
const DATA_EDGES = [0, 1, 2, 3, 4].map((i) => `M${col(i) + NODE_W} ${MID1}H${col(i + 1)}`);

// The measurement path runs back from Iceberg: DuckDB reads the snapshots and
// PIMS charts what it finds.
const QUERY_EDGES = [
  `M${cx(5)} ${ROW1 + NODE_H}V${MID2 - 14}Q${cx(5)} ${MID2} ${cx(5) - 14} ${MID2}H${col(1) + NODE_W}`,
  `M${col(1)} ${MID2}H${col(0) + NODE_W}`,
];

// Latency bracket from t0 (the row's updated_at, in MySQL) to t1 (the commit
// of the Iceberg snapshot that published it).
const BRACKET_Y = 36;
const BRACKET = `M${cx(1)} ${ROW1}V${BRACKET_Y}H${cx(5)}V${ROW1}`;

const SUMMARY =
  'Data flow: the simulator writes to MySQL, Debezium streams the changes to Kafka topics in Confluent Cloud, ' +
  'Tableflow materialises the topics as Iceberg tables, and DuckDB reads the Iceberg snapshots so PIMS can chart ' +
  'the time from a row\'s updated_at to the commit of the snapshot that published it.';

const STEPS = [
  {
    title: 'Stamp',
    text: <>MySQL sets <code>updated_at</code>, in UTC to the millisecond, on every insert and update. That is the row's start time, t0.</>,
  },
  {
    title: 'Replicate',
    text: <>Debezium streams the change to the table's Kafka topic and Tableflow writes it to Iceberg. Each Tableflow commit creates a snapshot with a commit time, t1.</>,
  },
  {
    title: 'Compare',
    text: <>Every 30 seconds DuckDB lists the snapshots committed since the last poll and compares each with the one before it, matching rows on <code>(id, updated_at)</code>. Rows only in the newer snapshot are the inserts and updates it published.</>,
  },
  {
    title: 'Measure',
    text: <>Latency is t1 minus the oldest <code>updated_at</code> among those rows: the longest any of them waited. The table view of the latency chart also shows the shortest wait.</>,
  },
];

export function DataFlow({active}: {active: boolean}) {
  return (
    <section className="card dataflow">
      <header className="card-head">
        <h2>Data flow</h2>
        <span className={active ? 'pill pill-on' : 'pill'}>{active ? '● Simulator running' : 'Simulator idle'}</span>
      </header>

      <div className="flow-scroll">
        <svg className={active ? 'flow flow-active' : 'flow'} viewBox="0 0 1000 296" role="img" aria-label={SUMMARY}>
          <defs>
            <marker id="flow-arrow-data" className="flow-arrow-data" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="8" markerHeight="8" markerUnits="userSpaceOnUse" orient="auto">
              <path d="M0 0L8 4L0 8z"/>
            </marker>
            <marker id="flow-arrow-query" className="flow-arrow-query" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="8" markerHeight="8" markerUnits="userSpaceOnUse" orient="auto">
              <path d="M0 0L8 4L0 8z"/>
            </marker>
          </defs>

          <g className="flow-group">
            <rect x={col(0) - 10} y={60} width={col(2) + NODE_W + 10 - (col(0) - 10)} height={222} rx={12}/>
            <text x={col(0)} y={80}>Your machine</text>
          </g>
          <g className="flow-group">
            <rect x={col(3) - 10} y={60} width={col(5) + NODE_W + 10 - (col(3) - 10)} height={116} rx={12}/>
            <text x={col(3)} y={80}>Confluent Cloud</text>
          </g>

          {/* Edges are paths, not lines: the stylesheet's `.grid line` rule would restyle <line> elements here. */}
          {DATA_EDGES.map((d) => <path key={d} className="edge edge-data" d={d} markerEnd="url(#flow-arrow-data)"/>)}
          {QUERY_EDGES.map((d) => <path key={d} className="edge edge-query" d={d} markerEnd="url(#flow-arrow-query)"/>)}
          <text className="edge-label" x={(col(1) + NODE_W + cx(5)) / 2 + 40} y={MID2 - 8} textAnchor="middle">
            Iceberg REST catalog · polled every 30 s
          </text>

          {NODES.map((n) => (
            <g key={n.title} className="flow-node">
              <rect x={col(n.col)} y={n.row} width={NODE_W} height={NODE_H} rx={8}/>
              <text className="node-title" x={col(n.col) + NODE_W / 2} y={n.row + 24} textAnchor="middle">{n.title}</text>
              <text className="node-sub" x={col(n.col) + NODE_W / 2} y={n.row + 40} textAnchor="middle">{n.lines[0]}</text>
              <text className="node-sub" x={col(n.col) + NODE_W / 2} y={n.row + 54} textAnchor="middle">{n.lines[1]}</text>
            </g>
          ))}

          <path className="bracket" d={BRACKET}/>
          <circle className="bracket-dot" cx={cx(1)} cy={ROW1} r={4}/>
          <circle className="bracket-dot" cx={cx(5)} cy={ROW1} r={4}/>
          <text className="bracket-end" x={cx(1) + 8} y={BRACKET_Y - 9}><tspan>t0</tspan> · row's updated_at</text>
          <text className="bracket-end" x={cx(5) - 8} y={BRACKET_Y - 9} textAnchor="end"><tspan>t1</tspan> · snapshot commit time</text>
          <text className="bracket-label" x={(cx(1) + cx(5)) / 2} y={BRACKET_Y - 9} textAnchor="middle">latency = t1 − t0</text>
        </svg>
      </div>

      <div className="latency-how">
        <h3>How latency is calculated</h3>
        <ol className="how-steps">
          {STEPS.map((s) => (
            <li key={s.title}>
              <strong>{s.title}</strong>
              <span>{s.text}</span>
            </li>
          ))}
        </ol>
        <p className="formula">
          latency = snapshot commit time − oldest <code>updated_at</code> among the rows it published
        </p>
        <ul className="fine how-notes">
          <li>
            Example: a snapshot committed at 10:01:10 that published rows updated at 10:00:05 and 10:00:40 plots
            as 65 s, and its shortest wait is 30 s.
          </li>
          <li>
            The figure covers the whole path: the MySQL write, Debezium capture, Kafka, Tableflow and the commit to
            storage. Polling does not add to it; it only decides when a point appears.
          </li>
          <li>
            Snapshots that publish no new rows, such as compaction, are skipped. A table's first snapshot is only a
            baseline, because its rows may predate Tableflow.
          </li>
          <li>
            <code>updated_at</code> comes from MySQL's clock inside Docker, so the figure is only as accurate as that clock.
          </li>
        </ul>
      </div>
    </section>
  );
}
