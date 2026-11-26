// Node and edge geometry is in the SVG's own 1000-unit coordinate space; the
// SVG scales to the card's width.
const NODE_W = 128;
const NODE_H = 64;
const STEP = 162; // column pitch: NODE_W plus a 34-unit gap for the arrow
const X0 = 31;
const GROUP_Y = 96;
const ROW1 = 124;
const ROW2 = 234;
const GROUP_H = ROW2 + NODE_H + 28 - GROUP_Y;
const VIEW_H = GROUP_Y + GROUP_H + 10;

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
  {col: 2, row: ROW2, title: 'Latency chart', lines: ['PIMS app', 'plots both metrics']},
  {col: 4, row: ROW2, title: 'Metrics API', lines: ['Confluent Cloud', 'freshness metrics']},
];

const MID1 = ROW1 + NODE_H / 2;
const MID2 = ROW2 + NODE_H / 2;

// The write path, left to right.
const DATA_EDGES = [0, 1, 2, 3, 4].map((i) => `M${col(i) + NODE_W} ${MID1}H${col(i + 1)}`);

// The metrics path: Tableflow reports to the Metrics API, and PIMS polls it.
const METRIC_EDGES = [
  `M${cx(4)} ${ROW1 + NODE_H}V${ROW2}`,
  `M${col(4)} ${MID2}H${col(2) + NODE_W}`,
];

// Brackets for the two metrics. Both end where Tableflow writes to the Iceberg
// table. Event freshness starts at the record's timestamp, which the record
// carries through the Kafka topic; processing freshness starts when Tableflow
// picks the record up.
const BRACKET_EVENT_Y = 30;
const BRACKET_PROCESSING_Y = 68;
const bracket = (from: number, y: number) => `M${cx(from)} ${ROW1}V${y}H${cx(5)}V${ROW1}`;

const SUMMARY =
  'Data flow: the simulator writes to MySQL, Debezium streams the changes to Kafka topics in Confluent Cloud, ' +
  'and Tableflow materialises the topics as Iceberg tables. Tableflow reports its event freshness and processing ' +
  'freshness latency to the Confluent Cloud Metrics API, and PIMS polls them and charts them.';

const STEPS = [
  {
    title: 'Replicate',
    text: <>Debezium streams each change to the table's Kafka topic as a record, and the record carries a timestamp.</>,
  },
  {
    title: 'Materialise',
    text: <>Tableflow picks records up from the topic and commits them to the Iceberg table, which creates a snapshot.</>,
  },
  {
    title: 'Report',
    text: <>Tableflow reports two freshness gauges per table to the Confluent Cloud Metrics API, updated when it commits.</>,
  },
  {
    title: 'Read',
    text: <>Every 60 seconds PIMS asks the Metrics API for the last 5 hours, a value per minute, and charts what Tableflow reported.</>,
  },
];

export function DataFlow({active}: {active: boolean}) {
  return (
    <section className="pane dataflow">
      <header className="panel-head">
        <h2>Data flow</h2>
        <span className={active ? 'pill ok' : 'pill'}>{active ? 'Simulator running' : 'Simulator idle'}</span>
      </header>

      <div className="flow-scroll">
        <svg className={active ? 'flow flow-active' : 'flow'} viewBox={`0 0 1000 ${VIEW_H}`} role="img" aria-label={SUMMARY}>
          <defs>
            <marker id="flow-arrow-data" className="flow-arrow-data" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="8" markerHeight="8" markerUnits="userSpaceOnUse" orient="auto">
              <path d="M0 0L8 4L0 8z"/>
            </marker>
            <marker id="flow-arrow-query" className="flow-arrow-query" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="8" markerHeight="8" markerUnits="userSpaceOnUse" orient="auto">
              <path d="M0 0L8 4L0 8z"/>
            </marker>
          </defs>

          {/* Captions sit on the bottom edge so the brackets' vertical lines do not cross them. */}
          <g className="flow-group">
            <rect x={col(0) - 10} y={GROUP_Y} width={col(2) + NODE_W + 10 - (col(0) - 10)} height={GROUP_H} rx={12}/>
            <text x={col(0)} y={GROUP_Y + GROUP_H - 10}>Your machine</text>
          </g>
          <g className="flow-group">
            <rect x={col(3) - 10} y={GROUP_Y} width={col(5) + NODE_W + 10 - (col(3) - 10)} height={GROUP_H} rx={12}/>
            <text x={col(3)} y={GROUP_Y + GROUP_H - 10}>Confluent Cloud</text>
          </g>

          {DATA_EDGES.map((d) => <path key={d} className="edge edge-data" d={d} markerEnd="url(#flow-arrow-data)"/>)}
          {METRIC_EDGES.map((d) => <path key={d} className="edge edge-query" d={d} markerEnd="url(#flow-arrow-query)"/>)}
          <text className="edge-label" x={(col(4) + col(2) + NODE_W) / 2} y={MID2 - 8} textAnchor="middle">
            polled every 60 s
          </text>

          {NODES.map((n) => (
            <g key={n.title} className="flow-node">
              <rect x={col(n.col)} y={n.row} width={NODE_W} height={NODE_H} rx={8}/>
              <text className="node-title" x={col(n.col) + NODE_W / 2} y={n.row + 24} textAnchor="middle">{n.title}</text>
              <text className="node-sub" x={col(n.col) + NODE_W / 2} y={n.row + 40} textAnchor="middle">{n.lines[0]}</text>
              <text className="node-sub" x={col(n.col) + NODE_W / 2} y={n.row + 54} textAnchor="middle">{n.lines[1]}</text>
            </g>
          ))}

          <path className="bracket" d={bracket(3, BRACKET_EVENT_Y)}/>
          <path className="bracket" d={bracket(4, BRACKET_PROCESSING_Y)}/>
          {[cx(3), cx(4), cx(5)].map((x) => <circle key={x} className="bracket-dot" cx={x} cy={ROW1} r={4}/>)}
          <text className="bracket-label" x={(cx(3) + cx(5)) / 2} y={BRACKET_EVENT_Y - 9} textAnchor="middle">event freshness</text>
          <text className="bracket-label" x={(cx(4) + cx(5)) / 2} y={BRACKET_PROCESSING_Y - 9} textAnchor="middle">processing freshness</text>
        </svg>
      </div>

      <div className="latency-how">
        <h3>What the metrics measure</h3>
        <ol className="how-steps">
          {STEPS.map((s) => (
            <li key={s.title}>
              <strong>{s.title}</strong>
              <span>{s.text}</span>
            </li>
          ))}
        </ol>
        <p className="formula">
          event freshness = when Tableflow wrote the record − the record's timestamp
        </p>
        <p className="formula">
          processing freshness = when Tableflow wrote the record − when it picked the record up
        </p>
        <ul className="small faint how-notes">
          <li>
            Event freshness is the age of the newest record Tableflow has written to the table, so it includes the whole
            path before Tableflow: the MySQL write, Debezium capture and Kafka. Processing freshness covers only
            Tableflow's own work.
          </li>
          <li>
            The values come from Tableflow, not from PIMS: PIMS does not read the Iceberg tables to work them out. Each
            point is the highest value Tableflow reported for that table in that minute.
          </li>
          <li>
            Tableflow updates the values when it commits to a table, so a flat stretch is normal. The Metrics API adds
            about three minutes of delay, and the first values can take several minutes to appear after Tableflow is
            enabled.
          </li>
          <li>Both metrics are in Preview and can change before general availability.</li>
        </ul>
      </div>
    </section>
  );
}
