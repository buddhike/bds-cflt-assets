-- PIMS schema. Every statement is idempotent so installation can be retried.
--
-- Conventions:
--   * Every table has a primary key: Debezium uses it as the Kafka record
--     key and Tableflow's upsert mode requires one.
--   * created_at / updated_at are DATETIME(3) set by MySQL (server time zone
--     is UTC). updated_at is the reference point for end-to-end latency.
--   * Business times (scheduled_at, check_in_at, ...) come from the
--     simulator's clock, not the wall clock.

CREATE TABLE IF NOT EXISTS patients (
  id             BIGINT       NOT NULL AUTO_INCREMENT,
  mrn            VARCHAR(16)  NOT NULL COMMENT 'Medical record number',
  first_name     VARCHAR(100) NOT NULL,
  last_name      VARCHAR(100) NOT NULL,
  date_of_birth  DATE         NOT NULL,
  sex            VARCHAR(1)   NOT NULL,
  phone          VARCHAR(32)  NULL,
  email          VARCHAR(255) NULL,
  address_line   VARCHAR(255) NULL,
  city           VARCHAR(100) NULL,
  state          VARCHAR(64)  NULL,
  postcode       VARCHAR(16)  NULL,
  created_at     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_patients_mrn (mrn),
  KEY ix_patients_updated_at (updated_at),
  CONSTRAINT ck_patients_sex CHECK (sex IN ('F', 'M', 'X'))
);

CREATE TABLE IF NOT EXISTS appointments (
  id               BIGINT       NOT NULL AUTO_INCREMENT,
  patient_id       BIGINT       NOT NULL,
  provider         VARCHAR(100) NOT NULL,
  department       VARCHAR(64)  NOT NULL,
  scheduled_at     DATETIME     NOT NULL,
  appointment_date DATE         NOT NULL,
  duration_minutes SMALLINT     NOT NULL,
  reason           VARCHAR(255) NULL,
  status           VARCHAR(16)  NOT NULL DEFAULT 'SCHEDULED',
  created_at       DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at       DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  -- Backstop for the simulator's rule: one appointment per patient per day.
  UNIQUE KEY uq_appointments_patient_day (patient_id, appointment_date),
  -- A provider cannot be double booked.
  UNIQUE KEY uq_appointments_provider_slot (provider, scheduled_at),
  KEY ix_appointments_status_scheduled (status, scheduled_at),
  KEY ix_appointments_updated_at (updated_at),
  CONSTRAINT fk_appointments_patient FOREIGN KEY (patient_id) REFERENCES patients (id),
  CONSTRAINT ck_appointments_date CHECK (appointment_date = DATE(scheduled_at)),
  CONSTRAINT ck_appointments_status CHECK (status IN ('SCHEDULED', 'CHECKED_IN', 'COMPLETED', 'CANCELLED', 'NO_SHOW'))
);

CREATE TABLE IF NOT EXISTS visits (
  id             BIGINT       NOT NULL AUTO_INCREMENT,
  patient_id     BIGINT       NOT NULL,
  appointment_id BIGINT       NULL COMMENT 'NULL for walk-in visits',
  visit_type     VARCHAR(16)  NOT NULL,
  provider       VARCHAR(100) NOT NULL,
  department     VARCHAR(64)  NOT NULL,
  reason         VARCHAR(255) NULL,
  status         VARCHAR(16)  NOT NULL DEFAULT 'IN_PROGRESS',
  check_in_at    DATETIME     NOT NULL,
  check_out_at   DATETIME     NULL,
  diagnosis_code VARCHAR(16)  NULL,
  created_at     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  -- An appointment results in at most one visit.
  UNIQUE KEY uq_visits_appointment (appointment_id),
  KEY ix_visits_status (status),
  KEY ix_visits_updated_at (updated_at),
  CONSTRAINT fk_visits_patient FOREIGN KEY (patient_id) REFERENCES patients (id),
  CONSTRAINT fk_visits_appointment FOREIGN KEY (appointment_id) REFERENCES appointments (id),
  CONSTRAINT ck_visits_type CHECK (visit_type IN ('SCHEDULED', 'WALK_IN', 'EMERGENCY')),
  CONSTRAINT ck_visits_status CHECK (status IN ('IN_PROGRESS', 'COMPLETED')),
  CONSTRAINT ck_visits_checkout CHECK (check_out_at IS NULL OR check_out_at >= check_in_at)
);
