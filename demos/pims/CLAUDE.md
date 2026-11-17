# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

PIMS (Patient Information Management System) is **spec-only so far**. The repo currently contains just `docs/specs/scope.md`, which is the source of truth for intended behavior. There is no code, build tooling, test suite, or git history yet, so there are no build/lint/test commands to document. Once the Wails app is scaffolded, add the real commands here (including how to run a single Go test) and replace the "Planned architecture" section with what was actually built.

## What this is

A demo app that showcases Confluent Cloud (CC) **Tableflow** with a **Debezium** CDC connector, materializing MySQL tables (patients, visits, appointments) as Iceberg tables. The app is a **Wails (Go) desktop app** that does three things:

1. Fully automates setup of every infrastructure component.
2. Simulates realistic patient registrations, visits and appointments.
3. Visualizes end-to-end replication latency.

## Planned architecture

Data path: `MySQL → Debezium connector → CC Kafka topics → Tableflow (Iceberg) → DuckDB query`

- **Cloud side (Terraform):** CC environment, Standard Kafka cluster, and one topic each for patients, visits and appointments. Tableflow uses Confluent managed storage.
- **Local side (Docker Compose):** MySQL, the Debezium connector, and DuckDB.
- **Wails app:** orchestrates both sides, then runs the simulator and the latency view.

Setup ordering matters: Terraform and Docker Compose come up first, and only then is the schema installed in MySQL.

## Constraints from the spec

- **Topic cleanup policy must be `compact`.** Tableflow with Debezium input requires it.
- **Embed Terraform via the `terraform-exec` Go library** so operators never install Terraform themselves. Docker is the only stated prerequisite.
- On launch, the app prompts for the AWS region and a CC API key and secret, used to provision the cloud resources.
- **Every table needs create/update timestamp columns.** End-to-end latency is measured from them.
- **Simulation must be realistic**, with operator-controlled event rate (events/sec). Example invariant: a patient cannot have more than one appointment on the same day. Enforce domain rules like this in the simulator, not just in the schema.
- **Latency visualization:** query the latest records with DuckDB, compare them with the timestamp of the source S3 file backing the Iceberg table, and plot the gap as a line graph.
