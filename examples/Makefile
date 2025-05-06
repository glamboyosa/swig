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
	@echo "Checking if PostgreSQL container exists..."
	@if [ "$$(docker ps -a -q -f name=$(CONTAINER_NAME))" ]; then \
		echo "Container $(CONTAINER_NAME) already exists"; \
		if [ "$$(docker ps -q -f name=$(CONTAINER_NAME))" ]; then \
			echo "Container is already running"; \
		else \
			echo "Starting existing container..."; \
			docker start $(CONTAINER_NAME); \
		fi; \
	else \
		echo "Creating new PostgreSQL container..."; \
		docker run --name $(CONTAINER_NAME) \
			-e POSTGRES_DB=$(DB_NAME) \
			-e POSTGRES_USER=$(DB_USER) \
			-e POSTGRES_PASSWORD=$(DB_PASSWORD) \
			-p $(DB_PORT):5432 \
			-d postgres:16; \
		echo "Waiting for PostgreSQL to start..."; \
		sleep 3; \
	fi

# Stop and remove PostgreSQL container
db-down:
	@echo "Stopping PostgreSQL..."
	@docker stop $(CONTAINER_NAME) || true
	@docker rm $(CONTAINER_NAME) || true

# Stop PostgreSQL container without removing it
db-stop:
	@echo "Stopping PostgreSQL..."
	@docker stop $(CONTAINER_NAME) || true

# Show PostgreSQL logs
db-logs:
	@docker logs $(CONTAINER_NAME)

# Show database tables
show-tables:
	@echo "Showing database tables..."
	@docker exec $(CONTAINER_NAME) psql -U $(DB_USER) -d $(DB_NAME) -c "\dt"
	@docker exec $(CONTAINER_NAME) psql -U $(DB_USER) -d $(DB_NAME) -c "SELECT * FROM swig_jobs;"

# Create database if it doesn't exist (relies on POSTGRES_DB in db-up, then waits for availability)
create-db:
	@echo "Ensuring PostgreSQL server is ready for user $(DB_USER)..."
	@until docker exec $(CONTAINER_NAME) pg_isready -U $(DB_USER) -q; do \
		echo "Waiting for PostgreSQL server to accept connections for user $(DB_USER)..."; \
		sleep 1; \
	done
	@echo "Waiting for database '$(DB_NAME)' (expected to be created by POSTGRES_DB='$(DB_NAME)' on container startup) to become fully available..."
	@until docker exec $(CONTAINER_NAME) psql -U $(DB_USER) -d $(DB_NAME) -c "" >/dev/null 2>&1; do \
		echo "Still waiting for database '$(DB_NAME)'..."; \
		sleep 1; \
	done
	@echo "Database '$(DB_NAME)' is now available."

# Run the pgx example
run-pgx: db-up create-db
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
	@echo "  make db-up      - Start PostgreSQL in Docker (creates or starts existing)"
	@echo "  make db-stop    - Stop PostgreSQL container without removing it"
	@echo "  make db-down    - Stop and remove PostgreSQL container"
	@echo "  make db-logs    - Show PostgreSQL logs"
	@echo "  make show-tables - Show database tables"
	@echo "  make run-pgx    - Run the PGX driver example"
	@echo "  make run-sql    - Run the SQL driver example"
	@echo "  make all        - Setup DB, dependencies and run PGX example (default)"