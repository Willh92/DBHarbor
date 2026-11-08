package sqlbase

import (
	"strings"

	"dbharbor/backend/internal/driver"
)

type sqlStateCarrier interface {
	SQLState() string
}

type mssqlNumberCarrier interface {
	SQLErrorNumber() int32
}

// IsAuthError inspects a driver error for a given database type and returns true ONLY when the failure
// indicates missing or invalid database authentication credentials (wrong username/password, SASL/SCRAM
// handshake rejection, or unauthenticated session). Post-login table/schema/command privilege errors
// (e.g. MySQL 1044/1142, Postgres 42501, ClickHouse 497, Redis NOPERM, MongoDB "not authorized on")
// strictly return false so normal RBAC denials never trigger credential re-prompts.
func IsAuthError(dbType string, err error) bool {
	if err == nil {
		return false
	}
	tp := strings.ToLower(strings.TrimSpace(dbType))
	if !driver.IsCredentialedDriver(tp) {
		return false
	}

	// Inspect typed error interfaces across wrapped error chains first.
	if matched, decided := inspectTypedAuthError(tp, err); decided {
		return matched
	}

	lower := strings.ToLower(err.Error())

	switch tp {
	case "mysql", "mariadb", "tidb", "oceanbase":
		if containsAny(lower,
			"error 1045",
			"errno 1045",
			"1045 (28000)",
			"error 1698",
			"errno 1698",
			"1698 (28000)",
		) {
			return true
		}
		if strings.Contains(lower, "command denied") ||
			strings.Contains(lower, "to database") ||
			strings.Contains(lower, "error 1044") ||
			strings.Contains(lower, "error 1142") ||
			strings.Contains(lower, "error 1143") ||
			strings.Contains(lower, "error 1227") {
			return false
		}
		if containsAny(lower,
			"(28000)",
			"sqlstate 28000",
			"access denied for user",
			"using password: yes",
			"using password: no",
		) {
			return true
		}
		return false

	case "postgres", "kingbase":
		if containsAny(lower, "sqlstate 42501", "permission denied for") {
			return false
		}
		return containsAny(lower,
			"sqlstate 28p01",
			"sqlstate 28000",
			"password authentication failed",
			"authentication failed for user",
			"sasl authentication failed",
			"fe_sendauth: no password supplied",
			"no password supplied",
		)

	case "clickhouse":
		if containsAny(lower, "code: 497", "not enough privileges") {
			return false
		}
		return containsAny(lower,
			"code: 516",
			"code: 192",
			"code: 193",
			"code: 194",
			"code: 515",
			"401 unauthorized",
			"401 code:",
			"http status code: 401",
			"authentication failed",
			"password is incorrect",
			"unknown user",
			"wrong password",
			"required password",
		)

	case "sqlserver":
		if containsAny(lower, "permission was denied", "cannot open database") && !strings.Contains(lower, "login failed") {
			return false
		}
		return containsAny(lower,
			"login failed for user",
			"error 18456",
			"mssql: login error",
		)

	case "oracle":
		if containsAny(lower, "ora-01031", "ora-00942", "insufficient privileges") {
			return false
		}
		return containsAny(lower,
			"ora-01017",
			"ora-01005",
			"ora-28000",
			"ora-28001",
			"invalid username/password",
			"null password given",
			"logon denied",
		)

	case "dameng":
		if containsAny(lower, "insufficient privileges", "没有查询权限", "没有权限") {
			return false
		}
		return containsAny(lower,
			"-2501",
			"error 2501",
			"code: 2501",
			"invalid username or password",
			"invalid user or password",
			"用户名或密码错误",
			"密码错误",
		)

	case "redis":
		if strings.Contains(lower, "noperm") {
			return false
		}
		return containsAny(lower,
			"wrongpass",
			"noauth",
			"err invalid password",
			"err invalid username-password",
			"client sent auth",
			"without any password configured",
			"authentication required",
		)

	case "mongodb":
		if strings.Contains(lower, "not authorized on") {
			return false
		}
		return containsAny(lower,
			"authenticationfailed",
			"authentication failed",
			"auth error:",
			"sasl conversation error",
			"unable to authenticate",
			"requires authentication",
		)
	}

	return false
}

func inspectTypedAuthError(tp string, err error) (matched bool, decided bool) {
	walkErrors(err, func(cur error) bool {
		switch tp {
		case "postgres", "kingbase":
			if sc, ok := cur.(sqlStateCarrier); ok {
				state := strings.ToUpper(strings.TrimSpace(sc.SQLState()))
				if state == "28P01" || state == "28000" {
					matched, decided = true, true
					return false
				}
				if state == "42501" {
					matched, decided = false, true
					return false
				}
			}
		case "sqlserver":
			if mc, ok := cur.(mssqlNumberCarrier); ok {
				num := mc.SQLErrorNumber()
				if num == 18456 || num == 18452 || num == 18470 {
					matched, decided = true, true
					return false
				}
				if num == 229 || num == 230 {
					matched, decided = false, true
					return false
				}
			}
		}
		return true
	})
	return matched, decided
}

func walkErrors(err error, visit func(error) bool) {
	if err == nil {
		return
	}
	if !visit(err) {
		return
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range multi.Unwrap() {
			walkErrors(child, visit)
		}
		return
	}
	if single, ok := err.(interface{ Unwrap() error }); ok {
		walkErrors(single.Unwrap(), visit)
	}
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
