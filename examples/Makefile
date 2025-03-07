.PHONY: run-pgx run-sql setup db-up db-down db-logs

# PostgreSQL configuration
DB_NAME=swig_example
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=postgres
CONTAINER_NAME=swig-postgres

# Default target
all: db-up setup run-pgx

# Setup dependencies
setup:
	go mod tidy

# Start PostgreSQL in Docker
db-up:
	@echo "Starting PostgreSQL in Docker..."
	@docker run --name $(CONTAINER_NAME) \
		-e POSTGRES_DB=$(DB_NAME) \
		-e POSTGRES_USER=$(DB_USER) \
		-e POSTGRES_PASSWORD=$(DB_PASSWORD) \
		-p $(DB_PORT):5432 \
		-d postgres:15
	@echo "Waiting for PostgreSQL to start..."
	@sleep 3

# Stop and remove PostgreSQL container
db-down:
	@echo "Stopping PostgreSQL..."
	@docker stop $(CONTAINER_NAME) || true
	@docker rm $(CONTAINER_NAME) || true

# Show PostgreSQL logs
db-logs:
	@docker logs $(CONTAINER_NAME)

# Show database tables
show-tables:
	@echo "Showing database tables..."
	@docker exec $(CONTAINER_NAME) psql -U $(DB_USER) -d $(DB_NAME) -c "\dt"
	@docker exec $(CONTAINER_NAME) psql -U $(DB_USER) -d $(DB_NAME) -c "SELECT * FROM swig_jobs;"

# Run the pgx example
run-pgx: db-up
	@echo "Running PGX example..."
	cd pgx && go run main.go

# Run the sql example
run-sql: db-up
	@echo "Running SQL example..."
	cd sql && go run main.go

# Help target
help:
	@echo "Available targets:"
	@echo "  make setup       - Install/update dependencies"
	@echo "  make db-up      - Start PostgreSQL in Docker"
	@echo "  make db-down    - Stop PostgreSQL container"
	@echo "  make db-logs    - Show PostgreSQL logs"
	@echo "  make show-tables - Show database tables"
	@echo "  make run-pgx    - Run the PGX driver example"
	@echo "  make run-sql    - Run the SQL driver example"
	@echo "  make all        - Setup DB, dependencies and run PGX example (default)"