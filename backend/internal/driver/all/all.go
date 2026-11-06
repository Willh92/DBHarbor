package all

import (
	// Register all 14 database drivers in clean display order
	_ "dbharbor/backend/internal/driver/clickhouse"
	_ "dbharbor/backend/internal/driver/mysql"
	_ "dbharbor/backend/internal/driver/postgres"
	_ "dbharbor/backend/internal/driver/sqlite"
	_ "dbharbor/backend/internal/driver/redis"
	_ "dbharbor/backend/internal/driver/mongodb"
	_ "dbharbor/backend/internal/driver/sqlserver"
	_ "dbharbor/backend/internal/driver/oracle"
	_ "dbharbor/backend/internal/driver/dameng"
)
