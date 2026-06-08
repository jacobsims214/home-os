#!/bin/sh
set -e

if [ -z "$DATABASE_URL" ]; then
    echo "ERROR: DATABASE_URL environment variable is required"
    exit 1
fi

echo "Running migrations..."
exec migrate -path /migrations -database "$DATABASE_URL" up
