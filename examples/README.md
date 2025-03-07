# Swig Examples

This directory contains example implementations of Swig using different PostgreSQL drivers.

## Prerequisites

1. Docker installed and running
2. Go 1.22 or later

## Running Examples

The examples use Docker to run PostgreSQL, so you don't need to install PostgreSQL locally. Use the Makefile to manage everything:

```bash
# Start PostgreSQL and run the PGX example
make run-pgx

# Start PostgreSQL and run the SQL example
make run-sql

# View database tables and jobs
make show-tables

# View PostgreSQL logs
make db-logs

# Stop PostgreSQL container
make db-down

# See all available commands
make help
```

## Database Connection

The examples connect to PostgreSQL with these default credentials:
- Host: localhost
- Port: 5432
- Database: swig_example
- Username: postgres
- Password: postgres

## What the Examples Demonstrate

Both examples show:
1. Basic setup and configuration
2. Worker implementation
3. Adding jobs to different queues
4. Scheduling jobs
5. Priority queues

The only difference is the database driver being used:
- `pgx/` uses the high-performance [pgx](https://github.com/jackc/pgx) driver
- `sql/` uses Go's standard `database/sql` with `lib/pq` 