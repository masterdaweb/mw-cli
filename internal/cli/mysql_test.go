package cli

import (
	"bytes"
	"strings"
	"testing"
)

// A dump as MySQL 8.4's mysqldump writes one from a GTID server, with a
// definer on every kind of stored object, and what --databases adds.
const mysqlDump = "-- MySQL dump 10.13  Distrib 8.4.13\n" +
	"/*M!999999\\- enable the sandbox mode */ \n" +
	"SET @MYSQLDUMP_TEMP_LOG_BIN = @@SESSION.SQL_LOG_BIN;\n" +
	"SET @@SESSION.SQL_LOG_BIN= 0;\n" +
	"SET @@GLOBAL.GTID_PURGED=/*!80000 '+'*/ '4d52e370-cff3-11f1-9c7d-d2f09efc7015:1-27,\n" +
	"9a1b2c3d-cff3-11f1-9c7d-d2f09efc7015:1-4';\n" +
	"CREATE DATABASE /*!32312 IF NOT EXISTS*/ `loja_antiga` /*!40100 DEFAULT CHARACTER SET utf8mb4 */;\n" +
	"USE `loja_antiga`;\n" +
	"CREATE TABLE `pedidos` (`id` int NOT NULL, `nota` varchar(40) DEFAULT 'DEFINER=x', PRIMARY KEY (`id`));\n" +
	"INSERT INTO `pedidos` VALUES (1,'Pão de queijo'),(2,'Café'),(3,'DEFINER=`a`@`b` is data');\n" +
	"/*!50001 CREATE ALGORITHM=UNDEFINED */\n" +
	"/*!50013 DEFINER=`app`@`%` SQL SECURITY DEFINER */\n" +
	"/*!50001 VIEW `pedidos_v` AS select `id` from `pedidos` */;\n" +
	"/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER `t` BEFORE INSERT ON `pedidos` FOR EACH ROW SET NEW.id = NEW.id */;;\n" +
	"CREATE DEFINER='app'@'%' PROCEDURE `limpar`() DELETE FROM pedidos;\n" +
	"SET @@SESSION.SQL_LOG_BIN = @MYSQLDUMP_TEMP_LOG_BIN;\n"

func TestFilterMySQLDumpLeavesOutWhatOnlyTheSourceCouldRun(t *testing.T) {
	var out bytes.Buffer
	rep, err := filterMySQLDump(strings.NewReader(mysqlDump), &out)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, gone := range []string{"GTID_PURGED", "9a1b2c3d", "SQL_LOG_BIN", "sandbox", "CREATE DATABASE", "USE `loja_antiga`", "DEFINER=`app`", "DEFINER=`root`", "DEFINER='app'"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is still in the dump:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"CREATE TABLE `pedidos`", "'Pão de queijo'", "(3,'DEFINER=`a`@`b` is data')", "SQL SECURITY DEFINER", "VIEW `pedidos_v`", "TRIGGER `t`", "CREATE PROCEDURE `limpar`", "DEFAULT 'DEFINER=x'"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q was lost:\n%s", kept, got)
		}
	}
	if rep.definers != 3 || rep.serverOnly != 4 || rep.otherDatabase != "loja_antiga" {
		t.Fatalf("report %+v", rep)
	}
}

func TestFilterMySQLDumpStreamsLongLines(t *testing.T) {
	// One extended INSERT longer than any buffer: copied whole.
	long := "INSERT INTO `t` VALUES " + strings.Repeat("(1,'xxxxxxxxxx'),", 400_000) + "(2,'y');\n"
	var out bytes.Buffer
	if _, err := filterMySQLDump(strings.NewReader(long+"-- end"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != long+"-- end" {
		t.Fatalf("a %d-byte line came out as %d bytes", len(long), out.Len()-len("-- end"))
	}
}

func TestBindingKindOfEachEngine(t *testing.T) {
	// An engine missing here once bound as POSTGRES, and the API answered
	// "sqlcheck is not a PostgreSQL database" for every SQL Server binding.
	for engine, want := range map[string]string{"postgres": "POSTGRES", "mysql": "MYSQL", "mariadb": "MYSQL", "valkey": "REDIS",
		"sqlserver": "SQLSERVER", "oracle": "ORACLE"} {
		if got := bindingKind(engine); got != want {
			t.Errorf("%s binds as %s, want %s", engine, got, want)
		}
	}
	if group("mariadb") != "db" || group("valkey") != "redis" || engineName("mariadb") != "MariaDB" {
		t.Fatal("names")
	}
}
