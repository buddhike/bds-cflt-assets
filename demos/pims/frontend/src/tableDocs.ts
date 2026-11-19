// What the schema helper beside the SQL window shows. It mirrors the tables in
// internal/schema/schema.sql, so change both together.

export interface ColumnDoc {
  name: string;
  /** The MySQL type. */
  type: string;
  nullable?: boolean;
  primaryKey?: boolean;
  /** `table.column` this column refers to. */
  references?: string;
  unique?: boolean;
  /** The only values the column holds, for a column with a CHECK on its values. */
  values?: string[];
  note?: string;
}

export interface TableDoc {
  name: string;
  summary: string;
  columns: ColumnDoc[];
}

const audit: ColumnDoc[] = [
  {name: 'created_at', type: 'DATETIME(3)', note: 'Set by MySQL, in UTC'},
  {name: 'updated_at', type: 'DATETIME(3)', note: 'Set by MySQL on every change, in UTC'},
];

export const TABLE_DOCS: TableDoc[] = [
  {
    name: 'patients',
    summary: 'One row per registered patient.',
    columns: [
      {name: 'id', type: 'BIGINT', primaryKey: true},
      {name: 'mrn', type: 'VARCHAR(16)', unique: true, note: 'Medical record number'},
      {name: 'first_name', type: 'VARCHAR(100)'},
      {name: 'last_name', type: 'VARCHAR(100)'},
      {name: 'date_of_birth', type: 'DATE'},
      {name: 'sex', type: 'VARCHAR(1)', values: ['F', 'M', 'X']},
      {name: 'phone', type: 'VARCHAR(32)', nullable: true},
      {name: 'email', type: 'VARCHAR(255)', nullable: true},
      {name: 'address_line', type: 'VARCHAR(255)', nullable: true},
      {name: 'city', type: 'VARCHAR(100)', nullable: true},
      {name: 'state', type: 'VARCHAR(64)', nullable: true},
      {name: 'postcode', type: 'VARCHAR(16)', nullable: true},
      ...audit,
    ],
  },
  {
    name: 'appointments',
    summary: 'Booked slots. A patient has one per day and a provider is never double booked.',
    columns: [
      {name: 'id', type: 'BIGINT', primaryKey: true},
      {name: 'patient_id', type: 'BIGINT', references: 'patients.id'},
      {name: 'provider', type: 'VARCHAR(100)'},
      {name: 'department', type: 'VARCHAR(64)'},
      {name: 'scheduled_at', type: 'DATETIME', note: 'Clinic clock'},
      {name: 'appointment_date', type: 'DATE', note: 'The date of scheduled_at'},
      {name: 'duration_minutes', type: 'SMALLINT'},
      {name: 'reason', type: 'VARCHAR(255)', nullable: true},
      {name: 'status', type: 'VARCHAR(16)', values: ['SCHEDULED', 'CHECKED_IN', 'COMPLETED', 'CANCELLED', 'NO_SHOW']},
      ...audit,
    ],
  },
  {
    name: 'visits',
    summary: 'Time spent in the clinic, from a booking or a walk-in.',
    columns: [
      {name: 'id', type: 'BIGINT', primaryKey: true},
      {name: 'patient_id', type: 'BIGINT', references: 'patients.id'},
      {name: 'appointment_id', type: 'BIGINT', nullable: true, unique: true, references: 'appointments.id', note: 'NULL for walk-in visits'},
      {name: 'visit_type', type: 'VARCHAR(16)', values: ['SCHEDULED', 'WALK_IN', 'EMERGENCY']},
      {name: 'provider', type: 'VARCHAR(100)'},
      {name: 'department', type: 'VARCHAR(64)'},
      {name: 'reason', type: 'VARCHAR(255)', nullable: true},
      {name: 'status', type: 'VARCHAR(16)', values: ['IN_PROGRESS', 'COMPLETED']},
      {name: 'check_in_at', type: 'DATETIME', note: 'Clinic clock'},
      {name: 'check_out_at', type: 'DATETIME', nullable: true, note: 'NULL while the visit is in progress'},
      {name: 'diagnosis_code', type: 'VARCHAR(16)', nullable: true},
      ...audit,
    ],
  },
];
