package mssql

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/go-xorm/xorm"
	"github.com/grafana/grafana/pkg/components/simplejson"
	"github.com/grafana/grafana/pkg/log"
	"github.com/grafana/grafana/pkg/services/sqlstore/sqlutil"
	"github.com/grafana/grafana/pkg/tsdb"
	. "github.com/smartystreets/goconvey/convey"
)

// To run this test, remove the Skip from SkipConvey
// and set up a MSSQL db named grafanatest and a user/password grafana/Password!
// Use the docker/blocks/mssql_tests/docker-compose.yaml to spin up a
// preconfigured MSSQL server suitable for running these tests.
// Thers's also a dashboard.json in same directory that you can import to Grafana
// once you've created a datasource for the test server/database.
// If needed, change the variable below to the IP address of the database.
var serverIP string = "localhost"

func TestMSSQL(t *testing.T) {
	SkipConvey("MSSQL", t, func() {
		x := InitMSSQLTestDB(t)

		endpoint := &MssqlQueryEndpoint{
			sqlEngine: &tsdb.DefaultSqlEngine{
				MacroEngine: NewMssqlMacroEngine(),
				XormEngine:  x,
			},
			log: log.New("tsdb.mssql"),
		}

		sess := x.NewSession()
		defer sess.Close()

		fromStart := time.Date(2018, 3, 15, 13, 0, 0, 0, time.UTC).In(time.Local)

		Convey("Given a table with different native data types", func() {
			sql := `
					IF OBJECT_ID('dbo.[mssql_types]', 'U') IS NOT NULL
						DROP TABLE dbo.[mssql_types]

					CREATE TABLE [mssql_types] (
						c_bit bit,

						c_tinyint tinyint,
						c_smallint smallint,
						c_int int,
						c_bigint bigint,

						c_money money,
						c_smallmoney smallmoney,
						c_numeric numeric(10,5),
						c_real real,
						c_decimal decimal(10,2),
						c_float float,

						c_char char(10),
						c_varchar varchar(10),
						c_text text,

						c_nchar nchar(12),
						c_nvarchar nvarchar(12),
						c_ntext ntext,

						c_datetime datetime,
						c_datetime2 datetime2,
						c_smalldatetime smalldatetime,
						c_date date,
						c_time time,
						c_datetimeoffset datetimeoffset
					)
				`

			_, err := sess.Exec(sql)
			So(err, ShouldBeNil)

			dt := time.Date(2018, 3, 14, 21, 20, 6, 527e6, time.UTC)
			dtFormat := "2006-01-02 15:04:05.999999999"
			d := dt.Format(dtFormat)
			dt2 := time.Date(2018, 3, 14, 21, 20, 6, 8896406e2, time.UTC)
			dt2Format := "2006-01-02 15:04:05.999999999 -07:00"
			d2 := dt2.Format(dt2Format)

			sql = fmt.Sprintf(`
				INSERT INTO [mssql_types]
				SELECT
		    1, 5, 20020, 980300, 1420070400, '$20000.15', '£2.15', 12345.12,
		    1.11, 2.22, 3.33,
					'char10', 'varchar10', 'text',
					N'☺nchar12☺', N'☺nvarchar12☺', N'☺text☺',
					CAST('%s' AS DATETIME), CAST('%s' AS DATETIME2), CAST('%s' AS SMALLDATETIME), CAST('%s' AS DATE), CAST('%s' AS TIME), SWITCHOFFSET(CAST('%s' AS DATETIMEOFFSET), '-07:00')
		`, d, d2, d, d, d, d2)

			_, err = sess.Exec(sql)
			So(err, ShouldBeNil)

			Convey("When doing a table query should map MSSQL column types to Go types", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": "SELECT * FROM mssql_types",
								"format": "table",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				queryResult := resp.Results["A"]
				So(err, ShouldBeNil)

				column := queryResult.Tables[0].Rows[0]

				So(column[0].(bool), ShouldEqual, true)

				So(column[1].(int64), ShouldEqual, 5)
				So(column[2].(int64), ShouldEqual, 20020)
				So(column[3].(int64), ShouldEqual, 980300)
				So(column[4].(int64), ShouldEqual, 1420070400)

				So(column[5].(float64), ShouldEqual, 20000.15)
				So(column[6].(float64), ShouldEqual, 2.15)
				So(column[7].(float64), ShouldEqual, 12345.12)
				So(column[8].(float64), ShouldEqual, 1.1100000143051147)
				So(column[9].(float64), ShouldEqual, 2.22)
				So(column[10].(float64), ShouldEqual, 3.33)

				So(column[11].(string), ShouldEqual, "char10    ")
				So(column[12].(string), ShouldEqual, "varchar10")
				So(column[13].(string), ShouldEqual, "text")

				So(column[14].(string), ShouldEqual, "☺nchar12☺   ")
				So(column[15].(string), ShouldEqual, "☺nvarchar12☺")
				So(column[16].(string), ShouldEqual, "☺text☺")

				So(column[17].(time.Time), ShouldEqual, dt)
				So(column[18].(time.Time), ShouldEqual, dt2)
				So(column[19].(time.Time), ShouldEqual, dt.Truncate(time.Minute))
				So(column[20].(time.Time), ShouldEqual, dt.Truncate(24*time.Hour))
				So(column[21].(time.Time), ShouldEqual, time.Date(1, 1, 1, dt.Hour(), dt.Minute(), dt.Second(), dt.Nanosecond(), time.UTC))
				So(column[22].(time.Time), ShouldEqual, dt2.In(time.FixedZone("UTC", int(-7*time.Hour))))
			})
		})

		Convey("Given a table with metrics that lacks data for some series ", func() {
			sql := `
					IF OBJECT_ID('dbo.[metric]', 'U') IS NOT NULL
						DROP TABLE dbo.[metric]

					CREATE TABLE [metric] (
						time datetime,
						value int
					)
				`

			_, err := sess.Exec(sql)
			So(err, ShouldBeNil)

			type metric struct {
				Time  time.Time
				Value int64
			}

			series := []*metric{}
			firstRange := genTimeRangeByInterval(fromStart, 10*time.Minute, 10*time.Second)
			secondRange := genTimeRangeByInterval(fromStart.Add(20*time.Minute), 10*time.Minute, 10*time.Second)

			for _, t := range firstRange {
				series = append(series, &metric{
					Time:  t,
					Value: 15,
				})
			}

			for _, t := range secondRange {
				series = append(series, &metric{
					Time:  t,
					Value: 20,
				})
			}

			_, err = sess.InsertMulti(series)
			So(err, ShouldBeNil)

			Convey("When doing a metric query using timeGroup", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": "SELECT $__timeGroup(time, '5m') AS time, avg(value) as value FROM metric GROUP BY $__timeGroup(time, '5m') ORDER BY 1",
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				points := queryResult.Series[0].Points
				So(len(points), ShouldEqual, 6)

				dt := fromStart

				for i := 0; i < 3; i++ {
					aValue := points[i][0].Float64
					aTime := time.Unix(int64(points[i][1].Float64)/1000, 0)
					So(aValue, ShouldEqual, 15)
					So(aTime, ShouldEqual, dt)
					dt = dt.Add(5 * time.Minute)
				}

				// adjust for 5 minute gap
				dt = dt.Add(5 * time.Minute)
				for i := 3; i < 6; i++ {
					aValue := points[i][0].Float64
					aTime := time.Unix(int64(points[i][1].Float64)/1000, 0)
					So(aValue, ShouldEqual, 20)
					So(aTime, ShouldEqual, dt)
					dt = dt.Add(5 * time.Minute)
				}
			})

			Convey("When doing a metric query using timeGroup with NULL fill enabled", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": "SELECT $__timeGroup(time, '5m', NULL) AS time, avg(value) as value FROM metric GROUP BY $__timeGroup(time, '5m') ORDER BY 1",
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
					TimeRange: &tsdb.TimeRange{
						From: fmt.Sprintf("%v", fromStart.Unix()*1000),
						To:   fmt.Sprintf("%v", fromStart.Add(34*time.Minute).Unix()*1000),
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				points := queryResult.Series[0].Points
				So(len(points), ShouldEqual, 7)

				dt := fromStart

				for i := 0; i < 3; i++ {
					aValue := points[i][0].Float64
					aTime := time.Unix(int64(points[i][1].Float64)/1000, 0)
					So(aValue, ShouldEqual, 15)
					So(aTime, ShouldEqual, dt)
					dt = dt.Add(5 * time.Minute)
				}

				So(points[3][0].Valid, ShouldBeFalse)

				// adjust for 5 minute gap
				dt = dt.Add(5 * time.Minute)
				for i := 4; i < 7; i++ {
					aValue := points[i][0].Float64
					aTime := time.Unix(int64(points[i][1].Float64)/1000, 0)
					So(aValue, ShouldEqual, 20)
					So(aTime, ShouldEqual, dt)
					dt = dt.Add(5 * time.Minute)
				}
			})

			Convey("When doing a metric query using timeGroup with float fill enabled", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": "SELECT $__timeGroup(time, '5m', 1.5) AS time, avg(value) as value FROM metric GROUP BY $__timeGroup(time, '5m') ORDER BY 1",
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
					TimeRange: &tsdb.TimeRange{
						From: fmt.Sprintf("%v", fromStart.Unix()*1000),
						To:   fmt.Sprintf("%v", fromStart.Add(34*time.Minute).Unix()*1000),
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				points := queryResult.Series[0].Points
				So(points[3][0].Float64, ShouldEqual, 1.5)
			})
		})

		Convey("Given a table with metrics having multiple values and measurements", func() {
			type metric_values struct {
				Time                time.Time
				TimeInt64           int64    `xorm:"bigint 'timeInt64' not null"`
				TimeInt64Nullable   *int64   `xorm:"bigint 'timeInt64Nullable' null"`
				TimeFloat64         float64  `xorm:"float 'timeFloat64' not null"`
				TimeFloat64Nullable *float64 `xorm:"float 'timeFloat64Nullable' null"`
				TimeInt32           int32    `xorm:"int(11) 'timeInt32' not null"`
				TimeInt32Nullable   *int32   `xorm:"int(11) 'timeInt32Nullable' null"`
				TimeFloat32         float32  `xorm:"float(11) 'timeFloat32' not null"`
				TimeFloat32Nullable *float32 `xorm:"float(11) 'timeFloat32Nullable' null"`
				Measurement         string
				ValueOne            int64 `xorm:"integer 'valueOne'"`
				ValueTwo            int64 `xorm:"integer 'valueTwo'"`
			}

			if exist, err := sess.IsTableExist(metric_values{}); err != nil || exist {
				So(err, ShouldBeNil)
				sess.DropTable(metric_values{})
			}
			err := sess.CreateTable(metric_values{})
			So(err, ShouldBeNil)

			rand.Seed(time.Now().Unix())
			rnd := func(min, max int64) int64 {
				return rand.Int63n(max-min) + min
			}

			var tInitial time.Time

			series := []*metric_values{}
			for i, t := range genTimeRangeByInterval(fromStart.Add(-30*time.Minute), 90*time.Minute, 5*time.Minute) {
				if i == 0 {
					tInitial = t
				}
				tSeconds := t.Unix()
				tSecondsInt32 := int32(tSeconds)
				tSecondsFloat32 := float32(tSeconds)
				tMilliseconds := tSeconds * 1e3
				tMillisecondsFloat := float64(tMilliseconds)
				first := metric_values{
					Time:                t,
					TimeInt64:           tMilliseconds,
					TimeInt64Nullable:   &(tMilliseconds),
					TimeFloat64:         tMillisecondsFloat,
					TimeFloat64Nullable: &tMillisecondsFloat,
					TimeInt32:           tSecondsInt32,
					TimeInt32Nullable:   &tSecondsInt32,
					TimeFloat32:         tSecondsFloat32,
					TimeFloat32Nullable: &tSecondsFloat32,
					Measurement:         "Metric A",
					ValueOne:            rnd(0, 100),
					ValueTwo:            rnd(0, 100),
				}
				second := first
				second.Measurement = "Metric B"
				second.ValueOne = rnd(0, 100)
				second.ValueTwo = rnd(0, 100)

				series = append(series, &first)
				series = append(series, &second)
			}

			_, err = sess.InsertMulti(series)
			So(err, ShouldBeNil)

			Convey("When doing a metric query using epoch (int64) as time column should return metric with time in milliseconds", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT TOP 1 timeInt64 as time, valueOne FROM metric_values ORDER BY time`,
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 1)
				So(queryResult.Series[0].Points[0][1].Float64, ShouldEqual, float64(tInitial.UnixNano()/1e6))
			})

			Convey("When doing a metric query using epoch (int64 nullable) as time column should return metric with time in milliseconds", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT TOP 1 timeInt64Nullable as time, valueOne FROM metric_values ORDER BY time`,
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 1)
				So(queryResult.Series[0].Points[0][1].Float64, ShouldEqual, float64(tInitial.UnixNano()/1e6))
			})

			Convey("When doing a metric query using epoch (float64) as time column should return metric with time in milliseconds", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT TOP 1 timeFloat64 as time, valueOne FROM metric_values ORDER BY time`,
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 1)
				So(queryResult.Series[0].Points[0][1].Float64, ShouldEqual, float64(tInitial.UnixNano()/1e6))
			})

			Convey("When doing a metric query using epoch (float64 nullable) as time column should return metric with time in milliseconds", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT TOP 1 timeFloat64Nullable as time, valueOne FROM metric_values ORDER BY time`,
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 1)
				So(queryResult.Series[0].Points[0][1].Float64, ShouldEqual, float64(tInitial.UnixNano()/1e6))
			})

			Convey("When doing a metric query using epoch (int32) as time column should return metric with time in milliseconds", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT TOP 1 timeInt32 as time, valueOne FROM metric_values ORDER BY time`,
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 1)
				So(queryResult.Series[0].Points[0][1].Float64, ShouldEqual, float64(tInitial.UnixNano()/1e6))
			})

			Convey("When doing a metric query using epoch (int32 nullable) as time column should return metric with time in milliseconds", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT TOP 1 timeInt32Nullable as time, valueOne FROM metric_values ORDER BY time`,
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 1)
				So(queryResult.Series[0].Points[0][1].Float64, ShouldEqual, float64(tInitial.UnixNano()/1e6))
			})

			Convey("When doing a metric query using epoch (float32) as time column should return metric with time in milliseconds", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT TOP 1 timeFloat32 as time, valueOne FROM metric_values ORDER BY time`,
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 1)
				So(queryResult.Series[0].Points[0][1].Float64, ShouldEqual, float64(float64(float32(tInitial.Unix())))*1e3)
			})

			Convey("When doing a metric query using epoch (float32 nullable) as time column should return metric with time in milliseconds", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT TOP 1 timeFloat32Nullable as time, valueOne FROM metric_values ORDER BY time`,
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 1)
				So(queryResult.Series[0].Points[0][1].Float64, ShouldEqual, float64(float64(float32(tInitial.Unix())))*1e3)
			})

			Convey("When doing a metric query grouping by time and select metric column should return correct series", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": "SELECT $__timeEpoch(time), measurement + ' - value one' as metric, valueOne FROM metric_values ORDER BY 1",
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 2)
				So(queryResult.Series[0].Name, ShouldEqual, "Metric A - value one")
				So(queryResult.Series[1].Name, ShouldEqual, "Metric B - value one")
			})

			Convey("When doing a metric query grouping by time should return correct series", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": "SELECT $__timeEpoch(time), valueOne, valueTwo FROM metric_values ORDER BY 1",
								"format": "time_series",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)

				So(len(queryResult.Series), ShouldEqual, 2)
				So(queryResult.Series[0].Name, ShouldEqual, "valueOne")
				So(queryResult.Series[1].Name, ShouldEqual, "valueTwo")
			})

			Convey("Given a stored procedure that takes @from and @to in epoch time", func() {
				sql := `
						IF object_id('sp_test_epoch') IS NOT NULL
							DROP PROCEDURE sp_test_epoch
					`

				_, err := sess.Exec(sql)
				So(err, ShouldBeNil)

				sql = `
						CREATE PROCEDURE sp_test_epoch(
							@from 		int,
							@to 			int,
							@interval nvarchar(50) = '5m',
							@metric 	nvarchar(200) = 'ALL'
						)	AS
						BEGIN
							DECLARE @dInterval int
							SELECT @dInterval = 300

							IF @interval = '10m'
								SELECT @dInterval = 600

							SELECT
								CAST(ROUND(DATEDIFF(second, '1970-01-01', time)/CAST(@dInterval as float), 0) as bigint)*@dInterval as time,
								measurement + ' - value one' as metric,
								avg(valueOne) as value
							FROM
								metric_values
							WHERE
								time BETWEEN DATEADD(s, @from, '1970-01-01') AND DATEADD(s, @to, '1970-01-01') AND
								(@metric = 'ALL' OR measurement = @metric)
							GROUP BY
								CAST(ROUND(DATEDIFF(second, '1970-01-01', time)/CAST(@dInterval as float), 0) as bigint)*@dInterval,
								measurement
							UNION ALL
							SELECT
								CAST(ROUND(DATEDIFF(second, '1970-01-01', time)/CAST(@dInterval as float), 0) as bigint)*@dInterval as time,
								measurement + ' - value two' as metric,
								avg(valueTwo) as value
							FROM
								metric_values
							WHERE
								time BETWEEN DATEADD(s, @from, '1970-01-01') AND DATEADD(s, @to, '1970-01-01') AND
								(@metric = 'ALL' OR measurement = @metric)
							GROUP BY
								CAST(ROUND(DATEDIFF(second, '1970-01-01', time)/CAST(@dInterval as float), 0) as bigint)*@dInterval,
								measurement
							ORDER BY 1
						END
					`

				_, err = sess.Exec(sql)
				So(err, ShouldBeNil)

				Convey("When doing a metric query using stored procedure should return correct result", func() {
					query := &tsdb.TsdbQuery{
						Queries: []*tsdb.Query{
							{
								Model: simplejson.NewFromAny(map[string]interface{}{
									"rawSql": `DECLARE
											@from int = $__unixEpochFrom(),
											@to int = $__unixEpochTo()

											EXEC dbo.sp_test_epoch @from, @to`,
									"format": "time_series",
								}),
								RefId: "A",
							},
						},
						TimeRange: &tsdb.TimeRange{
							From: "1521117000000",
							To:   "1521122100000",
						},
					}

					resp, err := endpoint.Query(nil, nil, query)
					queryResult := resp.Results["A"]
					So(err, ShouldBeNil)
					So(queryResult.Error, ShouldBeNil)

					So(len(queryResult.Series), ShouldEqual, 4)
					So(queryResult.Series[0].Name, ShouldEqual, "Metric A - value one")
					So(queryResult.Series[1].Name, ShouldEqual, "Metric B - value one")
					So(queryResult.Series[2].Name, ShouldEqual, "Metric A - value two")
					So(queryResult.Series[3].Name, ShouldEqual, "Metric B - value two")
				})
			})

			Convey("Given a stored procedure that takes @from and @to in datetime", func() {
				sql := `
						IF object_id('sp_test_datetime') IS NOT NULL
							DROP PROCEDURE sp_test_datetime
					`

				_, err := sess.Exec(sql)
				So(err, ShouldBeNil)

				sql = `
						CREATE PROCEDURE sp_test_datetime(
							@from 		datetime,
							@to 			datetime,
							@interval nvarchar(50) = '5m',
							@metric 	nvarchar(200) = 'ALL'
						)	AS
						BEGIN
							DECLARE @dInterval int
							SELECT @dInterval = 300

							IF @interval = '10m'
								SELECT @dInterval = 600

							SELECT
								CAST(ROUND(DATEDIFF(second, '1970-01-01', time)/CAST(@dInterval as float), 0) as bigint)*@dInterval as time,
								measurement + ' - value one' as metric,
								avg(valueOne) as value
							FROM
								metric_values
							WHERE
								time BETWEEN @from AND @to AND
								(@metric = 'ALL' OR measurement = @metric)
							GROUP BY
								CAST(ROUND(DATEDIFF(second, '1970-01-01', time)/CAST(@dInterval as float), 0) as bigint)*@dInterval,
								measurement
							UNION ALL
							SELECT
								CAST(ROUND(DATEDIFF(second, '1970-01-01', time)/CAST(@dInterval as float), 0) as bigint)*@dInterval as time,
								measurement + ' - value two' as metric,
								avg(valueTwo) as value
							FROM
								metric_values
							WHERE
								time BETWEEN @from AND @to AND
								(@metric = 'ALL' OR measurement = @metric)
							GROUP BY
								CAST(ROUND(DATEDIFF(second, '1970-01-01', time)/CAST(@dInterval as float), 0) as bigint)*@dInterval,
								measurement
							ORDER BY 1
						END
					`

				_, err = sess.Exec(sql)
				So(err, ShouldBeNil)

				Convey("When doing a metric query using stored procedure should return correct result", func() {
					query := &tsdb.TsdbQuery{
						Queries: []*tsdb.Query{
							{
								Model: simplejson.NewFromAny(map[string]interface{}{
									"rawSql": `DECLARE
											@from int = $__unixEpochFrom(),
											@to int = $__unixEpochTo()

											EXEC dbo.sp_test_epoch @from, @to`,
									"format": "time_series",
								}),
								RefId: "A",
							},
						},
						TimeRange: &tsdb.TimeRange{
							From: "1521117000000",
							To:   "1521122100000",
						},
					}

					resp, err := endpoint.Query(nil, nil, query)
					queryResult := resp.Results["A"]
					So(err, ShouldBeNil)
					So(queryResult.Error, ShouldBeNil)

					So(len(queryResult.Series), ShouldEqual, 4)
					So(queryResult.Series[0].Name, ShouldEqual, "Metric A - value one")
					So(queryResult.Series[1].Name, ShouldEqual, "Metric B - value one")
					So(queryResult.Series[2].Name, ShouldEqual, "Metric A - value two")
					So(queryResult.Series[3].Name, ShouldEqual, "Metric B - value two")
				})
			})
		})

		Convey("Given a table with event data", func() {
			sql := `
				IF OBJECT_ID('dbo.[event]', 'U') IS NOT NULL
					DROP TABLE dbo.[event]

				CREATE TABLE [event] (
					time_sec int,
					description nvarchar(100),
					tags nvarchar(100),
				)
			`

			_, err := sess.Exec(sql)
			So(err, ShouldBeNil)

			type event struct {
				TimeSec     int64
				Description string
				Tags        string
			}

			events := []*event{}
			for _, t := range genTimeRangeByInterval(fromStart.Add(-20*time.Minute), 60*time.Minute, 25*time.Minute) {
				events = append(events, &event{
					TimeSec:     t.Unix(),
					Description: "Someone deployed something",
					Tags:        "deploy",
				})
				events = append(events, &event{
					TimeSec:     t.Add(5 * time.Minute).Unix(),
					Description: "New support ticket registered",
					Tags:        "ticket",
				})
			}

			for _, e := range events {
				sql = fmt.Sprintf(`
					INSERT [event] (time_sec, description, tags)
					VALUES(%d, '%s', '%s')
				`, e.TimeSec, e.Description, e.Tags)

				_, err = sess.Exec(sql)
				So(err, ShouldBeNil)
			}

			Convey("When doing an annotation query of deploy events should return expected result", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": "SELECT time_sec as time, description as [text], tags FROM [event] WHERE $__unixEpochFilter(time_sec) AND tags='deploy' ORDER BY 1 ASC",
								"format": "table",
							}),
							RefId: "Deploys",
						},
					},
					TimeRange: &tsdb.TimeRange{
						From: fmt.Sprintf("%v", fromStart.Add(-20*time.Minute).Unix()*1000),
						To:   fmt.Sprintf("%v", fromStart.Add(40*time.Minute).Unix()*1000),
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				queryResult := resp.Results["Deploys"]
				So(err, ShouldBeNil)
				So(len(queryResult.Tables[0].Rows), ShouldEqual, 3)
			})

			Convey("When doing an annotation query of ticket events should return expected result", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": "SELECT time_sec as time, description as [text], tags FROM [event] WHERE $__unixEpochFilter(time_sec) AND tags='ticket' ORDER BY 1 ASC",
								"format": "table",
							}),
							RefId: "Tickets",
						},
					},
					TimeRange: &tsdb.TimeRange{
						From: fmt.Sprintf("%v", fromStart.Add(-20*time.Minute).Unix()*1000),
						To:   fmt.Sprintf("%v", fromStart.Add(40*time.Minute).Unix()*1000),
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				queryResult := resp.Results["Tickets"]
				So(err, ShouldBeNil)
				So(len(queryResult.Tables[0].Rows), ShouldEqual, 3)
			})

			Convey("When doing an annotation query with a time column in datetime format", func() {
				dt := time.Date(2018, 3, 14, 21, 20, 6, 527e6, time.UTC)
				dtFormat := "2006-01-02 15:04:05.999999999"

				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": fmt.Sprintf(`SELECT
									CAST('%s' AS DATETIME) as time,
									'message' as text,
									'tag1,tag2' as tags
								`, dt.Format(dtFormat)),
								"format": "table",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)
				So(len(queryResult.Tables[0].Rows), ShouldEqual, 1)
				columns := queryResult.Tables[0].Rows[0]

				//Should be in milliseconds
				So(columns[0].(float64), ShouldEqual, float64(dt.UnixNano()/1e6))
			})

			Convey("When doing an annotation query with a time column in epoch second format should return ms", func() {
				dt := time.Date(2018, 3, 14, 21, 20, 6, 527e6, time.UTC)

				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": fmt.Sprintf(`SELECT
									 %d as time,
									'message' as text,
									'tag1,tag2' as tags
								`, dt.Unix()),
								"format": "table",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)
				So(len(queryResult.Tables[0].Rows), ShouldEqual, 1)
				columns := queryResult.Tables[0].Rows[0]

				//Should be in milliseconds
				So(columns[0].(int64), ShouldEqual, int64(dt.Unix()*1000))
			})

			Convey("When doing an annotation query with a time column in epoch second format (int) should return ms", func() {
				dt := time.Date(2018, 3, 14, 21, 20, 6, 527e6, time.UTC)

				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": fmt.Sprintf(`SELECT
									 cast(%d as int) as time,
									'message' as text,
									'tag1,tag2' as tags
								`, dt.Unix()),
								"format": "table",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)
				So(len(queryResult.Tables[0].Rows), ShouldEqual, 1)
				columns := queryResult.Tables[0].Rows[0]

				//Should be in milliseconds
				So(columns[0].(int64), ShouldEqual, int64(dt.Unix()*1000))
			})

			Convey("When doing an annotation query with a time column in epoch millisecond format should return ms", func() {
				dt := time.Date(2018, 3, 14, 21, 20, 6, 527e6, time.UTC)

				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": fmt.Sprintf(`SELECT
									 %d as time,
									'message' as text,
									'tag1,tag2' as tags
								`, dt.Unix()*1000),
								"format": "table",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)
				So(len(queryResult.Tables[0].Rows), ShouldEqual, 1)
				columns := queryResult.Tables[0].Rows[0]

				//Should be in milliseconds
				So(columns[0].(float64), ShouldEqual, float64(dt.Unix()*1000))
			})

			Convey("When doing an annotation query with a time column holding a bigint null value should return nil", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT
									 cast(null as bigint) as time,
									'message' as text,
									'tag1,tag2' as tags
								`,
								"format": "table",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)
				So(len(queryResult.Tables[0].Rows), ShouldEqual, 1)
				columns := queryResult.Tables[0].Rows[0]

				//Should be in milliseconds
				So(columns[0], ShouldBeNil)
			})

			Convey("When doing an annotation query with a time column holding a datetime null value should return nil", func() {
				query := &tsdb.TsdbQuery{
					Queries: []*tsdb.Query{
						{
							Model: simplejson.NewFromAny(map[string]interface{}{
								"rawSql": `SELECT
									 cast(null as datetime) as time,
									'message' as text,
									'tag1,tag2' as tags
								`,
								"format": "table",
							}),
							RefId: "A",
						},
					},
				}

				resp, err := endpoint.Query(nil, nil, query)
				So(err, ShouldBeNil)
				queryResult := resp.Results["A"]
				So(queryResult.Error, ShouldBeNil)
				So(len(queryResult.Tables[0].Rows), ShouldEqual, 1)
				columns := queryResult.Tables[0].Rows[0]

				//Should be in milliseconds
				So(columns[0], ShouldBeNil)
			})
		})
	})
}

func InitMSSQLTestDB(t *testing.T) *xorm.Engine {
	x, err := xorm.NewEngine(sqlutil.TestDB_Mssql.DriverName, strings.Replace(sqlutil.TestDB_Mssql.ConnStr, "localhost", serverIP, 1))
	if err != nil {
		t.Fatalf("Failed to init mssql db %v", err)
	}

	x.DatabaseTZ = time.UTC
	x.TZLocation = time.UTC

	// x.ShowSQL()

	return x
}

func genTimeRangeByInterval(from time.Time, duration time.Duration, interval time.Duration) []time.Time {
	durationSec := int64(duration.Seconds())
	intervalSec := int64(interval.Seconds())
	timeRange := []time.Time{}

	for i := int64(0); i < durationSec; i += intervalSec {
		timeRange = append(timeRange, from)
		from = from.Add(time.Duration(int64(time.Second) * intervalSec))
	}

	return timeRange
}
