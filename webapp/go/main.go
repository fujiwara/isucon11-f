package main

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/go-sql-driver/mysql"
	"github.com/goccy/go-json"
	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sync/singleflight"
)

const (
	SQLDirectory              = "../sql/"
	AssignmentsDirectory      = "../assignments/"
	InitDataDirectory         = "../data/"
	SessionName               = "isucholar_go"
	mysqlErrNumDuplicateEntry = 1062
)

type handlers struct {
	DB    *sqlx.DB
	Redis *redis.Client
}

var teacherNameCache = sync.Map{}
var courseCache = sync.Map{}

type JSONSerializer struct{}

func (j *JSONSerializer) Serialize(c echo.Context, i interface{}, indent string) error {
	enc := json.NewEncoder(c.Response())
	return enc.Encode(i)
}

func (j *JSONSerializer) Deserialize(c echo.Context, i interface{}) error {
	err := json.NewDecoder(c.Request().Body).Decode(i)
	if ute, ok := err.(*json.UnmarshalTypeError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Unmarshal type error: expected=%v, got=%v, field=%v, offset=%v", ute.Type, ute.Value, ute.Field, ute.Offset)).SetInternal(err)
	} else if se, ok := err.(*json.SyntaxError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Syntax error: offset=%v, error=%v", se.Offset, se.Error())).SetInternal(err)
	}
	return err
}

func newRedis() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     GetEnv("REDIS_ADDR", "127.0.0.1:6379"),
		Password: "",
		DB:       0,
	})
}

func main() {
	e := echo.New()
	e.JSONSerializer = &JSONSerializer{}
	e.Debug = GetEnv("DEBUG", "") == "true"
	e.Server.Addr = fmt.Sprintf(":%v", GetEnv("PORT", "7000"))
	e.HideBanner = true

	//e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(session.Middleware(sessions.NewCookieStore([]byte("trapnomura"))))

	db, _ := GetDB(false)
	db.SetMaxOpenConns(40)

	redisClient := redis.NewClient(&redis.Options{
		Addr:     GetEnv("REDIS_ADDR", "127.0.0.1:6379"),
		Password: "",
		DB:       0,
	})

	h := &handlers{
		DB:    db,
		Redis: redisClient,
	}

	e.POST("/initialize", h.Initialize)

	e.POST("/login", h.Login)
	e.POST("/logout", h.Logout)
	API := e.Group("/api", h.IsLoggedIn)
	{
		usersAPI := API.Group("/users")
		{
			usersAPI.GET("/me", h.GetMe)
			usersAPI.GET("/me/courses", h.GetRegisteredCourses)
			usersAPI.PUT("/me/courses", h.RegisterCourses)
			usersAPI.GET("/me/grades", h.GetGrades)
		}
		coursesAPI := API.Group("/courses")
		{
			coursesAPI.GET("", h.SearchCourses)
			coursesAPI.POST("", h.AddCourse, h.IsAdmin)
			coursesAPI.GET("/:courseID", h.GetCourseDetail)
			coursesAPI.PUT("/:courseID/status", h.SetCourseStatus, h.IsAdmin)
			coursesAPI.GET("/:courseID/classes", h.GetClasses)
			coursesAPI.POST("/:courseID/classes", h.AddClass, h.IsAdmin)
			coursesAPI.POST("/:courseID/classes/:classID/assignments", h.SubmitAssignment)
			coursesAPI.PUT("/:courseID/classes/:classID/assignments/scores", h.RegisterScores, h.IsAdmin)
			coursesAPI.GET("/:courseID/classes/:classID/assignments/export", h.DownloadSubmittedAssignments, h.IsAdmin)
		}
		announcementsAPI := API.Group("/announcements")
		{
			announcementsAPI.GET("", h.GetAnnouncementList)
			announcementsAPI.POST("", h.AddAnnouncement, h.IsAdmin)
			announcementsAPI.GET("/:announcementID", h.GetAnnouncementDetail)
		}
	}

	e.Logger.Error(e.StartServer(e.Server))
}

type InitializeResponse struct {
	Language string `json:"language"`
}

// Initialize POST /initialize ??????????????????????????????
func (h *handlers) Initialize(c echo.Context) error {
	dbForInit, _ := GetDB(true)

	files := []string{
		"1_schema.sql",
		"2_init.sql",
		"3_sample.sql",
	}
	for _, file := range files {
		data, err := os.ReadFile(SQLDirectory + file)
		if err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		if _, err := dbForInit.Exec(string(data)); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
	}

	if err := exec.Command("rm", "-rf", AssignmentsDirectory).Run(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if err := exec.Command("cp", "-r", InitDataDirectory, AssignmentsDirectory).Run(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	rc := newRedis()
	rc.FlushAll(context.TODO())
	rc.SAdd(context.TODO(), "unread_announcements:01FF4RXEKS0DG2EG20CN2GJB8K", "01FF4RXEKS0DG2EG20DDPCS14P")
	rc.SAdd(context.TODO(), "registrations:01FF4RXEKS0DG2EG20CWPQ60M3", "01FF4RXEKS0DG2EG20CN2GJB8K", "01FF4RXEKS0DG2EG20CQVX6FV0", "01FF4RXEKS0DG2EG20CTTAPEVH")
	rc.SAdd(context.TODO(), "registrations:01FF4RXEKS0DG2EG20CYAYCCGM", "01FF4RXEKS0DG2EG20CN2GJB8K")

	res := InitializeResponse{
		Language: "go",
	}
	return c.JSON(http.StatusOK, res)
}

// IsLoggedIn ?????????????????????middleware
func (h *handlers) IsLoggedIn(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		sess, err := session.Get(SessionName, c)
		if err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		if sess.IsNew {
			return c.String(http.StatusUnauthorized, "You are not logged in.")
		}
		_, ok := sess.Values["userID"]
		if !ok {
			return c.String(http.StatusUnauthorized, "You are not logged in.")
		}

		return next(c)
	}
}

// IsAdmin admin?????????middleware
func (h *handlers) IsAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		sess, err := session.Get(SessionName, c)
		if err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		isAdmin, ok := sess.Values["isAdmin"]
		if !ok {
			c.Logger().Error("failed to get isAdmin from session")
			return c.NoContent(http.StatusInternalServerError)
		}
		if !isAdmin.(bool) {
			return c.String(http.StatusForbidden, "You are not admin user.")
		}

		return next(c)
	}
}

func getUserInfo(c echo.Context) (userID string, userName string, isAdmin bool, userCode string, err error) {
	sess, err := session.Get(SessionName, c)
	if err != nil {
		return "", "", false, "", err
	}
	_userID, ok := sess.Values["userID"]
	if !ok {
		return "", "", false, "", errors.New("failed to get userID from session")
	}
	_userName, ok := sess.Values["userName"]
	if !ok {
		return "", "", false, "", errors.New("failed to get userName from session")
	}
	_isAdmin, ok := sess.Values["isAdmin"]
	if !ok {
		return "", "", false, "", errors.New("failed to get isAdmin from session")
	}
	_userCode, ok := sess.Values["userCode"]
	if !ok {
		return "", "", false, "", errors.New("failed to get userCode from session")
	}
	return _userID.(string), _userName.(string), _isAdmin.(bool), _userCode.(string), nil
}

type UserType string

const (
	Student UserType = "student"
	Teacher UserType = "teacher"
)

type User struct {
	ID             string   `db:"id"`
	Code           string   `db:"code"`
	Name           string   `db:"name"`
	HashedPassword []byte   `db:"hashed_password"`
	Type           UserType `db:"type"`
}

type UserIDAndCode struct {
	ID   string `db:"id"`
	Code string `db:"code"`
}

type CourseType string

const (
	LiberalArts   CourseType = "liberal-arts"
	MajorSubjects CourseType = "major-subjects"
)

type DayOfWeek string

const (
	Monday    DayOfWeek = "monday"
	Tuesday   DayOfWeek = "tuesday"
	Wednesday DayOfWeek = "wednesday"
	Thursday  DayOfWeek = "thursday"
	Friday    DayOfWeek = "friday"
)

var daysOfWeek = []DayOfWeek{Monday, Tuesday, Wednesday, Thursday, Friday}

type CourseStatus string

const (
	StatusRegistration CourseStatus = "registration"
	StatusInProgress   CourseStatus = "in-progress"
	StatusClosed       CourseStatus = "closed"
)

type Course struct {
	ID          string       `db:"id"`
	Code        string       `db:"code"`
	Type        CourseType   `db:"type"`
	Name        string       `db:"name"`
	Description string       `db:"description"`
	Credit      uint8        `db:"credit"`
	Period      uint8        `db:"period"`
	DayOfWeek   DayOfWeek    `db:"day_of_week"`
	TeacherID   string       `db:"teacher_id"`
	Keywords    string       `db:"keywords"`
	Status      CourseStatus `db:"status"`
}

// ---------- Public API ----------

type LoginRequest struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

// Login POST /login ????????????
func (h *handlers) Login(c echo.Context) error {
	var req LoginRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	var user User
	if err := h.DB.Get(&user, "SELECT * FROM `users` WHERE `code` = ?", req.Code); err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusUnauthorized, "Code or Password is wrong.")
	}

	if bcrypt.CompareHashAndPassword(user.HashedPassword, []byte(req.Password)) != nil {
		return c.String(http.StatusUnauthorized, "Code or Password is wrong.")
	}

	sess, err := session.Get(SessionName, c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if userID, ok := sess.Values["userID"].(string); ok && userID == user.ID {
		return c.String(http.StatusBadRequest, "You are already logged in.")
	}

	sess.Values["userID"] = user.ID
	sess.Values["userName"] = user.Name
	sess.Values["isAdmin"] = user.Type == Teacher
	sess.Values["userCode"] = user.Code
	sess.Options = &sessions.Options{
		Path:   "/",
		MaxAge: 3600,
	}

	if err := sess.Save(c.Request(), c.Response()); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusOK)
}

// Logout POST /logout ???????????????
func (h *handlers) Logout(c echo.Context) error {
	sess, err := session.Get(SessionName, c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	sess.Options = &sessions.Options{
		Path:   "/",
		MaxAge: -1,
	}

	if err := sess.Save(c.Request(), c.Response()); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusOK)
}

// ---------- Users API ----------

type GetMeResponse struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"is_admin"`
}

// GetMe GET /api/users/me ????????????????????????
func (h *handlers) GetMe(c echo.Context) error {
	_, userName, isAdmin, userCode, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.JSON(http.StatusOK, GetMeResponse{
		Code:    userCode,
		Name:    userName,
		IsAdmin: isAdmin,
	})
}

type GetRegisteredCourseResponseContent struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Teacher   string    `json:"teacher"`
	Period    uint8     `json:"period"`
	DayOfWeek DayOfWeek `json:"day_of_week"`
}

// GetRegisteredCourses GET /api/users/me/courses ??????????????????????????????
func (h *handlers) GetRegisteredCourses(c echo.Context) error {
	userID, _, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var courses []Course
	query := "SELECT `courses`.*" +
		" FROM `courses`" +
		" JOIN `registrations` ON `courses`.`id` = `registrations`.`course_id`" +
		" WHERE `courses`.`status` != ? AND `registrations`.`user_id` = ?"
	if err := h.DB.Select(&courses, query, StatusClosed, userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	// ???????????????0??????????????????????????????
	res := make([]GetRegisteredCourseResponseContent, 0, len(courses))
	for _, course := range courses {
		var teacher User
		var teacherName string
		if name, found := teacherNameCache.Load(course.TeacherID); found {
			teacherName = name.(string)
		} else {
			if err := h.DB.Get(&teacher, "SELECT name FROM `users` WHERE `id` = ?", course.TeacherID); err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}
			teacherName = teacher.Name
			teacherNameCache.Store(course.TeacherID, teacherName)
		}

		res = append(res, GetRegisteredCourseResponseContent{
			ID:        course.ID,
			Name:      course.Name,
			Teacher:   teacherName,
			Period:    course.Period,
			DayOfWeek: course.DayOfWeek,
		})
	}

	return c.JSON(http.StatusOK, res)
}

type RegisterCourseRequestContent struct {
	ID string `json:"id"`
}

type RegisterCoursesErrorResponse struct {
	CourseNotFound       []string `json:"course_not_found,omitempty"`
	NotRegistrableStatus []string `json:"not_registrable_status,omitempty"`
	ScheduleConflict     []string `json:"schedule_conflict,omitempty"`
}

// RegisterCourses PUT /api/users/me/courses ????????????
func (h *handlers) RegisterCourses(c echo.Context) error {
	userID, _, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var req []RegisterCourseRequestContent
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}
	sort.Slice(req, func(i, j int) bool {
		return req[i].ID < req[j].ID
	})

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var errors RegisterCoursesErrorResponse
	var newlyAdded []Course
	for _, courseReq := range req {
		courseID := courseReq.ID
		var course Course
		if cs, found := courseCache.Load(courseID); found {
			course = cs.(Course)
		} else {
			if err := tx.Get(&course, "SELECT * FROM `courses` WHERE `id` = ? FOR SHARE", courseID); err != nil && err != sql.ErrNoRows {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			} else if err == sql.ErrNoRows {
				errors.CourseNotFound = append(errors.CourseNotFound, courseReq.ID)
				continue
			}
			courseCache.Store(courseID, course)
		}
		if course.Status != StatusRegistration {
			errors.NotRegistrableStatus = append(errors.NotRegistrableStatus, course.ID)
			continue
		}

		// ???????????????????????????????????????????????????
		var count int
		if err := tx.Get(&count, "SELECT COUNT(*) FROM `registrations` WHERE `course_id` = ? AND `user_id` = ?", course.ID, userID); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		if count > 0 {
			continue
		}

		newlyAdded = append(newlyAdded, course)
	}

	var alreadyRegistered []Course
	query := "SELECT `courses`.*" +
		" FROM `courses`" +
		" JOIN `registrations` ON `courses`.`id` = `registrations`.`course_id`" +
		" WHERE `courses`.`status` != ? AND `registrations`.`user_id` = ?"
	if err := tx.Select(&alreadyRegistered, query, StatusClosed, userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	alreadyRegistered = append(alreadyRegistered, newlyAdded...)
	for _, course1 := range newlyAdded {
		for _, course2 := range alreadyRegistered {
			if course1.ID != course2.ID && course1.Period == course2.Period && course1.DayOfWeek == course2.DayOfWeek {
				errors.ScheduleConflict = append(errors.ScheduleConflict, course1.ID)
				break
			}
		}
	}

	if len(errors.CourseNotFound) > 0 || len(errors.NotRegistrableStatus) > 0 || len(errors.ScheduleConflict) > 0 {
		return c.JSON(http.StatusBadRequest, errors)
	}
	regArgs := make([]interface{}, 0, len(newlyAdded)*2)
	for _, course := range newlyAdded {
		regArgs = append(regArgs, course.ID)
		regArgs = append(regArgs, userID)
		if err := h.Redis.SAdd(context.TODO(), "registrations:"+course.ID, userID).Err(); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
	}

	_, err = tx.Exec(
		"INSERT IGNORE INTO `registrations` (`course_id`, `user_id`) "+
			"VALUES (?, ?)"+strings.Repeat(",(?,?)", len(newlyAdded)-1), regArgs...,
	)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if err = tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusOK)
}

type Class struct {
	ID               string `db:"id"`
	CourseID         string `db:"course_id"`
	Part             uint8  `db:"part"`
	Title            string `db:"title"`
	Description      string `db:"description"`
	SubmissionClosed bool   `db:"submission_closed"`
}

type GetGradeResponse struct {
	Summary       Summary        `json:"summary"`
	CourseResults []CourseResult `json:"courses"`
}

type Summary struct {
	Credits   int     `json:"credits"`
	GPA       float64 `json:"gpa"`
	GpaTScore float64 `json:"gpa_t_score"` // ?????????
	GpaAvg    float64 `json:"gpa_avg"`     // ?????????
	GpaMax    float64 `json:"gpa_max"`     // ?????????
	GpaMin    float64 `json:"gpa_min"`     // ?????????
}

type CourseResult struct {
	Name             string       `json:"name"`
	Code             string       `json:"code"`
	TotalScore       int          `json:"total_score"`
	TotalScoreTScore float64      `json:"total_score_t_score"` // ?????????
	TotalScoreAvg    float64      `json:"total_score_avg"`     // ?????????
	TotalScoreMax    int          `json:"total_score_max"`     // ?????????
	TotalScoreMin    int          `json:"total_score_min"`     // ?????????
	ClassScores      []ClassScore `json:"class_scores"`
}

type ClassScore struct {
	ClassID    string `json:"class_id"`
	Title      string `json:"title"`
	Part       uint8  `json:"part"`
	Score      *int   `json:"score"`      // 0~100???
	Submitters int    `json:"submitters"` // ?????????????????????
}

var gpaCachedAt time.Time
var cachedGPAs []float64
var gpaCalcGroup singleflight.Group

// map by course ID
var totalScoreCachedAt = sync.Map{}  // map[string]time.Time
var cachedTotalScore = sync.Map{}    // map[string][]int
var totalScoreCalcGroup = sync.Map{} // map[string]*singleflight.Group

// GetGrades GET /api/users/me/grades ????????????
func (h *handlers) GetGrades(c echo.Context) error {
	userID, _, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// GPA????????????
	// ????????????????????????????????????????????????GPA??????
	now := time.Now()
	var gpas []float64
	if now.Sub(gpaCachedAt) > 900*time.Millisecond || cachedGPAs == nil {
		gpasIf, err, _ := gpaCalcGroup.Do("gpaCalc", func() (interface{}, error) {
			var newGPAs []float64
			q := "SELECT IFNULL(SUM(`submissions`.`score` * `courses`.`credit`), 0) / 100 / `credits`.`credits` AS `gpa`" +
				" FROM `users`" +
				" JOIN (" +
				"     SELECT `users`.`id` AS `user_id`, SUM(`courses`.`credit`) AS `credits`" +
				"     FROM `users`" +
				"     JOIN `registrations` ON `users`.`id` = `registrations`.`user_id`" +
				"     JOIN `courses` ON `registrations`.`course_id` = `courses`.`id` AND `courses`.`status` = ?" +
				"     GROUP BY `users`.`id`" +
				" ) AS `credits` ON `credits`.`user_id` = `users`.`id`" +
				" JOIN `registrations` ON `users`.`id` = `registrations`.`user_id`" +
				" JOIN `courses` ON `registrations`.`course_id` = `courses`.`id` AND `courses`.`status` = ?" +
				" LEFT JOIN `classes` ON `courses`.`id` = `classes`.`course_id`" +
				" LEFT JOIN `submissions` ON `users`.`id` = `submissions`.`user_id` AND `submissions`.`class_id` = `classes`.`id`" +
				" WHERE `users`.`type` = ?" +
				" GROUP BY `users`.`id`"
			if err := h.DB.Select(&newGPAs, q, StatusClosed, StatusClosed, Student); err != nil {
				return nil, err
			}
			return newGPAs, nil
		})
		if err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		gpas = gpasIf.([]float64)
		cachedGPAs = gpas
		gpaCachedAt = now // time.Now() ???????????????????????????????????????????????????
	} else {
		gpas = cachedGPAs
	}

	// ????????????????????????????????????
	var registeredCourses []Course
	query := "SELECT `courses`.*" +
		" FROM `registrations`" +
		" JOIN `courses` ON `registrations`.`course_id` = `courses`.`id`" +
		" WHERE `user_id` = ? ORDER BY `course_id`"
	if err := h.DB.Select(&registeredCourses, query, userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// ??????????????????????????????
	courseResults := make([]CourseResult, 0, len(registeredCourses))
	myGPA := 0.0
	myCredits := 0
	for _, course := range registeredCourses {
		// ?????????????????????
		var classes []Class
		query = "SELECT *" +
			" FROM `classes`" +
			" WHERE `course_id` = ?" +
			" ORDER BY `part` DESC"
		if err := h.DB.Select(&classes, query, course.ID); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}

		// ??????????????????????????????
		classScores := make([]ClassScore, 0, len(classes))
		classIDs := []string{}
		for _, class := range classes {
			classIDs = append(classIDs, class.ID)
		}

		var myTotalScore int
		submissionsCounts := map[string]int64{}
		myScores := map[string]sql.NullInt64{}
		if len(classIDs) > 0 {
			q, args, err := sqlx.In("SELECT class_id, COUNT(*) FROM `submissions` WHERE `class_id` IN (?) GROUP BY `class_id`", classIDs)
			if err != nil {
				c.Logger().Error(err) // oops
				return c.NoContent(http.StatusInternalServerError)
			}
			rows, err := h.DB.Query(q, args...)
			if err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}

			for rows.Next() {
				var classID string
				var count int64
				err := rows.Scan(&classID, &count)
				if err != nil {
					c.Logger().Error(err)
				}
				submissionsCounts[classID] = count
			}
			rows.Close()

			q, args, err = sqlx.In("SELECT `class_id`, `submissions`.`score` FROM `submissions` WHERE `user_id` = ? AND `class_id` IN (?)", userID, classIDs)
			if err != nil {
				c.Logger().Error(err) // oops
				return c.NoContent(http.StatusInternalServerError)
			}
			rows, err = h.DB.Query(q, args...)
			if err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}

			for rows.Next() {
				var classID string
				var score sql.NullInt64
				err := rows.Scan(&classID, &score)
				if err != nil {
					c.Logger().Error(err)
				}
				myScores[classID] = score
			}
			rows.Close()
		}

		for _, class := range classes {
			classID := class.ID
			subCount := int(submissionsCounts[classID])
			nullScore := myScores[classID]
			if !nullScore.Valid {
				classScores = append(classScores, ClassScore{
					ClassID:    classID,
					Part:       class.Part,
					Title:      class.Title,
					Score:      nil,
					Submitters: subCount,
				})
			} else {
				score := int(nullScore.Int64)
				myTotalScore += score
				classScores = append(classScores, ClassScore{
					ClassID:    classID,
					Part:       class.Part,
					Title:      class.Title,
					Score:      &score,
					Submitters: subCount,
				})
			}
		}

		// ??????????????????????????????????????????TotalScore???????????????
		var totals []int
		cachedAt, found := totalScoreCachedAt.Load(course.ID)
		score, found2 := cachedTotalScore.Load(course.ID)
		if !found || !found2 || now.Sub(cachedAt.(time.Time)) > 100*time.Millisecond {
			flightIf, found3 := totalScoreCalcGroup.Load(course.ID)
			var flight *singleflight.Group
			if !found3 {
				flight = &singleflight.Group{}
			} else {
				flight = flightIf.(*singleflight.Group)
			}
			totalScoreCalcGroup.Store(course.ID, flight)
			totalsIf, err, _ := flight.Do(fmt.Sprintf("totalScore-%s", course.ID), func() (interface{}, error) {
				var newTotals []int
				q := "SELECT IFNULL(SUM(`submissions`.`score`), 0) AS `total_score`" +
					" FROM `users`" +
					" JOIN `registrations` ON `users`.`id` = `registrations`.`user_id`" +
					" JOIN `courses` ON `registrations`.`course_id` = `courses`.`id`" +
					" LEFT JOIN `classes` ON `courses`.`id` = `classes`.`course_id`" +
					" LEFT JOIN `submissions` ON `users`.`id` = `submissions`.`user_id` AND `submissions`.`class_id` = `classes`.`id`" +
					" WHERE `courses`.`id` = ?" +
					" GROUP BY `users`.`id`"
				if err := h.DB.Select(&newTotals, q, course.ID); err != nil {
					return nil, err
				}

				return newTotals, nil
			})
			if err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}

			totals = totalsIf.([]int)
			cachedTotalScore.Store(course.ID, totals)
			totalScoreCachedAt.Store(course.ID, now)
		} else {
			totals = score.([]int)
		}

		courseResults = append(courseResults, CourseResult{
			Name:             course.Name,
			Code:             course.Code,
			TotalScore:       myTotalScore,
			TotalScoreTScore: tScoreInt(myTotalScore, totals),
			TotalScoreAvg:    averageInt(totals, 0),
			TotalScoreMax:    maxInt(totals, 0),
			TotalScoreMin:    minInt(totals, 0),
			ClassScores:      classScores,
		})

		// ?????????GPA??????
		if course.Status == StatusClosed {
			myGPA += float64(myTotalScore * int(course.Credit))
			myCredits += int(course.Credit)
		}
	}
	if myCredits > 0 {
		myGPA = myGPA / 100 / float64(myCredits)
	}

	res := GetGradeResponse{
		Summary: Summary{
			Credits:   myCredits,
			GPA:       myGPA,
			GpaTScore: tScoreFloat64(myGPA, gpas),
			GpaAvg:    averageFloat64(gpas, 0),
			GpaMax:    maxFloat64(gpas, 0),
			GpaMin:    minFloat64(gpas, 0),
		},
		CourseResults: courseResults,
	}

	return c.JSON(http.StatusOK, res)
}

// ---------- Courses API ----------

// SearchCourses GET /api/courses ????????????
func (h *handlers) SearchCourses(c echo.Context) error {
	query := "SELECT `courses`.*, `users`.`name` AS `teacher`" +
		" FROM `courses` JOIN `users` ON `courses`.`teacher_id` = `users`.`id`" +
		" WHERE 1=1"
	var condition string
	var args []interface{}

	// ???????????????????????????????????????????????????????????????

	if courseType := c.QueryParam("type"); courseType != "" {
		condition += " AND `courses`.`type` = ?"
		args = append(args, courseType)
	}

	if credit, err := strconv.Atoi(c.QueryParam("credit")); err == nil && credit > 0 {
		condition += " AND `courses`.`credit` = ?"
		args = append(args, credit)
	}

	if teacher := c.QueryParam("teacher"); teacher != "" {
		condition += " AND `users`.`name` = ?"
		args = append(args, teacher)
	}

	if period, err := strconv.Atoi(c.QueryParam("period")); err == nil && period > 0 {
		condition += " AND `courses`.`period` = ?"
		args = append(args, period)
	}

	if dayOfWeek := c.QueryParam("day_of_week"); dayOfWeek != "" {
		condition += " AND `courses`.`day_of_week` = ?"
		args = append(args, dayOfWeek)
	}

	if keywords := c.QueryParam("keywords"); keywords != "" {
		arr := strings.Split(keywords, " ")
		var nameCondition string
		for _, keyword := range arr {
			nameCondition += " AND `courses`.`name` LIKE ?"
			args = append(args, "%"+keyword+"%")
		}
		var keywordsCondition string
		for _, keyword := range arr {
			keywordsCondition += " AND `courses`.`keywords` LIKE ?"
			args = append(args, "%"+keyword+"%")
		}
		condition += fmt.Sprintf(" AND ((1=1%s) OR (1=1%s))", nameCondition, keywordsCondition)
	}

	if status := c.QueryParam("status"); status != "" {
		condition += " AND `courses`.`status` = ?"
		args = append(args, status)
	}

	condition += " ORDER BY `courses`.`code`"

	var page int
	if c.QueryParam("page") == "" {
		page = 1
	} else {
		var err error
		page, err = strconv.Atoi(c.QueryParam("page"))
		if err != nil || page <= 0 {
			return c.String(http.StatusBadRequest, "Invalid page.")
		}
	}
	limit := 20
	offset := limit * (page - 1)

	// limit??????????????????????????????????????????limit?????????????????????????????????????????????????????????????????????????????????
	condition += " LIMIT ? OFFSET ?"
	args = append(args, limit+1, offset)

	// ?????????0??????????????????????????????
	res := make([]GetCourseDetailResponse, 0)
	if err := h.DB.Select(&res, query+condition, args...); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var links []string
	linkURL, err := url.Parse(c.Request().URL.Path + "?" + c.Request().URL.RawQuery)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	q := linkURL.Query()
	if page > 1 {
		q.Set("page", strconv.Itoa(page-1))
		linkURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf("<%v>; rel=\"prev\"", linkURL))
	}
	if len(res) > limit {
		q.Set("page", strconv.Itoa(page+1))
		linkURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf("<%v>; rel=\"next\"", linkURL))
	}
	if len(links) > 0 {
		c.Response().Header().Set("Link", strings.Join(links, ","))
	}

	if len(res) == limit+1 {
		res = res[:len(res)-1]
	}

	return c.JSON(http.StatusOK, res)
}

type AddCourseRequest struct {
	Code        string     `json:"code"`
	Type        CourseType `json:"type"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Credit      int        `json:"credit"`
	Period      int        `json:"period"`
	DayOfWeek   DayOfWeek  `json:"day_of_week"`
	Keywords    string     `json:"keywords"`
}

type AddCourseResponse struct {
	ID string `json:"id"`
}

// AddCourse POST /api/courses ??????????????????
func (h *handlers) AddCourse(c echo.Context) error {
	userID, _, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var req AddCourseRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	if req.Type != LiberalArts && req.Type != MajorSubjects {
		return c.String(http.StatusBadRequest, "Invalid course type.")
	}
	if !contains(daysOfWeek, req.DayOfWeek) {
		return c.String(http.StatusBadRequest, "Invalid day of week.")
	}

	courseID := newULID()
	_, err = h.DB.Exec("INSERT INTO `courses` (`id`, `code`, `type`, `name`, `description`, `credit`, `period`, `day_of_week`, `teacher_id`, `keywords`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		courseID, req.Code, req.Type, req.Name, req.Description, req.Credit, req.Period, req.DayOfWeek, userID, req.Keywords)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == uint16(mysqlErrNumDuplicateEntry) {
			var course Course
			if err := h.DB.Get(&course, "SELECT * FROM `courses` WHERE `code` = ?", req.Code); err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}
			if req.Type != course.Type || req.Name != course.Name || req.Description != course.Description || req.Credit != int(course.Credit) || req.Period != int(course.Period) || req.DayOfWeek != course.DayOfWeek || req.Keywords != course.Keywords {
				return c.String(http.StatusConflict, "A course with the same code already exists.")
			}
			return c.JSON(http.StatusCreated, AddCourseResponse{ID: course.ID})
		}
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	c.Response().Header().Set("Cache-Control", "max-age=60")
	return c.JSON(http.StatusCreated, AddCourseResponse{ID: courseID})
}

type GetCourseDetailResponse struct {
	ID          string       `json:"id" db:"id"`
	Code        string       `json:"code" db:"code"`
	Type        string       `json:"type" db:"type"`
	Name        string       `json:"name" db:"name"`
	Description string       `json:"description" db:"description"`
	Credit      uint8        `json:"credit" db:"credit"`
	Period      uint8        `json:"period" db:"period"`
	DayOfWeek   string       `json:"day_of_week" db:"day_of_week"`
	TeacherID   string       `json:"-" db:"teacher_id"`
	Keywords    string       `json:"keywords" db:"keywords"`
	Status      CourseStatus `json:"status" db:"status"`
	Teacher     string       `json:"teacher" db:"teacher"`
}

// GetCourseDetail GET /api/courses/:courseID ?????????????????????
func (h *handlers) GetCourseDetail(c echo.Context) error {
	courseID := c.Param("courseID")

	var res GetCourseDetailResponse
	query := "SELECT `courses`.*, `users`.`name` AS `teacher`" +
		" FROM `courses`" +
		" JOIN `users` ON `courses`.`teacher_id` = `users`.`id`" +
		" WHERE `courses`.`id` = ?"
	if err := h.DB.Get(&res, query, courseID); err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusNotFound, "No such course.")
	}

	return c.JSON(http.StatusOK, res)
}

type SetCourseStatusRequest struct {
	Status CourseStatus `json:"status"`
}

// SetCourseStatus PUT /api/courses/:courseID/status ?????????????????????????????????
func (h *handlers) SetCourseStatus(c echo.Context) error {
	courseID := c.Param("courseID")

	var req SetCourseStatusRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var course Course
	if err := tx.Get(&course, "SELECT * FROM `courses` WHERE `id` = ? FOR UPDATE", courseID); err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusNotFound, "No such course.")
	}

	if _, err := tx.Exec("UPDATE `courses` SET `status` = ? WHERE `id` = ?", req.Status, courseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	course.Status = req.Status
	courseCache.Store(courseID, course)

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusOK)
}

type ClassWithSubmitted struct {
	ID               string `db:"id"`
	CourseID         string `db:"course_id"`
	Part             uint8  `db:"part"`
	Title            string `db:"title"`
	Description      string `db:"description"`
	SubmissionClosed bool   `db:"submission_closed"`
	Submitted        bool   `db:"submitted"`
}

type GetClassResponse struct {
	ID               string `json:"id"`
	Part             uint8  `json:"part"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	SubmissionClosed bool   `json:"submission_closed"`
	Submitted        bool   `json:"submitted"`
}

// GetClasses GET /api/courses/:courseID/classes ???????????????????????????????????????
func (h *handlers) GetClasses(c echo.Context) error {
	userID, _, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	courseID := c.Param("courseID")

	tx := h.DB
	var count int
	if err := tx.Get(&count, "SELECT COUNT(*) FROM `courses` WHERE `id` = ?", courseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if count == 0 {
		return c.String(http.StatusNotFound, "No such course.")
	}

	var classes []ClassWithSubmitted
	query := "SELECT `classes`.*, `submissions`.`user_id` IS NOT NULL AS `submitted`" +
		" FROM `classes`" +
		" LEFT JOIN `submissions` ON `classes`.`id` = `submissions`.`class_id` AND `submissions`.`user_id` = ?" +
		" WHERE `classes`.`course_id` = ?" +
		" ORDER BY `classes`.`part`"
	if err := tx.Select(&classes, query, userID, courseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// ?????????0??????????????????????????????
	res := make([]GetClassResponse, 0, len(classes))
	for _, class := range classes {
		res = append(res, GetClassResponse{
			ID:               class.ID,
			Part:             class.Part,
			Title:            class.Title,
			Description:      class.Description,
			SubmissionClosed: class.SubmissionClosed,
			Submitted:        class.Submitted,
		})
	}

	return c.JSON(http.StatusOK, res)
}

type AddClassRequest struct {
	Part        uint8  `json:"part"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type AddClassResponse struct {
	ClassID string `json:"class_id"`
}

// AddClass POST /api/courses/:courseID/classes ????????????(&??????)??????
func (h *handlers) AddClass(c echo.Context) error {
	courseID := c.Param("courseID")

	var req AddClassRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var course Course
	if err := tx.Get(&course, "SELECT * FROM `courses` WHERE `id` = ? FOR SHARE", courseID); err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusNotFound, "No such course.")
	}
	if course.Status != StatusInProgress {
		return c.String(http.StatusBadRequest, "This course is not in-progress.")
	}

	classID := newULID()
	if _, err := tx.Exec("INSERT INTO `classes` (`id`, `course_id`, `part`, `title`, `description`) VALUES (?, ?, ?, ?, ?)",
		classID, courseID, req.Part, req.Title, req.Description); err != nil {
		_ = tx.Rollback()
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == uint16(mysqlErrNumDuplicateEntry) {
			var class Class
			if err := h.DB.Get(&class, "SELECT * FROM `classes` WHERE `course_id` = ? AND `part` = ?", courseID, req.Part); err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}
			if req.Title != class.Title || req.Description != class.Description {
				return c.String(http.StatusConflict, "A class with the same part already exists.")
			}
			return c.JSON(http.StatusCreated, AddClassResponse{ClassID: class.ID})
		}
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.JSON(http.StatusCreated, AddClassResponse{ClassID: classID})
}

// SubmitAssignment POST /api/courses/:courseID/classes/:classID/assignments ???????????????
func (h *handlers) SubmitAssignment(c echo.Context) error {
	userID, _, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	courseID := c.Param("courseID")
	classID := c.Param("classID")

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var status CourseStatus
	if err := tx.Get(&status, "SELECT `status` FROM `courses` WHERE `id` = ? FOR SHARE", courseID); err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusNotFound, "No such course.")
	}
	if status != StatusInProgress {
		return c.String(http.StatusBadRequest, "This course is not in progress.")
	}

	var registrationCount int
	if err := tx.Get(&registrationCount, "SELECT COUNT(*) FROM `registrations` WHERE `user_id` = ? AND `course_id` = ?", userID, courseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if registrationCount == 0 {
		return c.String(http.StatusBadRequest, "You have not taken this course.")
	}

	var submissionClosed bool
	if err := tx.Get(&submissionClosed, "SELECT `submission_closed` FROM `classes` WHERE `id` = ? FOR SHARE", classID); err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusNotFound, "No such class.")
	}
	if submissionClosed {
		return c.String(http.StatusBadRequest, "Submission has been closed for this class.")
	}

	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return c.String(http.StatusBadRequest, "Invalid file.")
	}
	defer file.Close()

	if _, err := tx.Exec("INSERT INTO `submissions` (`user_id`, `class_id`, `file_name`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `file_name` = VALUES(`file_name`)", userID, classID, header.Filename); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	dst := AssignmentsDirectory + classID + "-" + userID + ".pdf"
	if err := os.WriteFile(dst, data, 0666); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusNoContent)
}

type Score struct {
	UserCode string `json:"user_code"`
	Score    int    `json:"score"`
}

// RegisterScores PUT /api/courses/:courseID/classes/:classID/assignments/scores ??????????????????
func (h *handlers) RegisterScores(c echo.Context) error {
	classID := c.Param("classID")

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var submissionClosed bool
	if err := tx.Get(&submissionClosed, "SELECT `submission_closed` FROM `classes` WHERE `id` = ? FOR SHARE", classID); err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusNotFound, "No such class.")
	}

	if !submissionClosed {
		return c.String(http.StatusBadRequest, "This assignment is not closed yet.")
	}

	var req []Score
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	if len(req) == 0 {
		if err := tx.Commit(); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		return c.NoContent(http.StatusNoContent)
	}

	userCodeMap := make(map[string]string, len(req))
	userCodes := make([]string, 0, len(req))
	for _, score := range req {
		if _, ok := userCodeMap[score.UserCode]; !ok {
			userCodeMap[score.UserCode] = ""
			userCodes = append(userCodes, score.UserCode)
		}
	}
	var userIDAndCode []UserIDAndCode
	ucq, args, err := sqlx.In("SELECT `id`, `code` FROM `users` WHERE code IN (?)", userCodes)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if err := tx.Select(&userIDAndCode, ucq, args...); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	for _, uc := range userIDAndCode {
		userCodeMap[uc.Code] = uc.ID
	}

	for _, score := range req {
		userID := userCodeMap[score.UserCode]
		if _, err := tx.Exec("UPDATE `submissions` SET `score` = ? WHERE `user_id` = ? AND `class_id` = ?", score.Score, userID, classID); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
	}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusNoContent)
}

type Submission struct {
	UserID   string `db:"user_id"`
	UserCode string `db:"user_code"`
	FileName string `db:"file_name"`
}

// DownloadSubmittedAssignments GET /api/courses/:courseID/classes/:classID/assignments/export ????????????????????????????????????zip?????????????????????????????????
func (h *handlers) DownloadSubmittedAssignments(c echo.Context) error {
	classID := c.Param("classID")

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var classCount int
	if err := tx.Get(&classCount, "SELECT COUNT(*) FROM `classes` WHERE `id` = ? FOR UPDATE", classID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if classCount == 0 {
		return c.String(http.StatusNotFound, "No such class.")
	}
	var submissions []Submission
	query := "SELECT `submissions`.`user_id`, `submissions`.`file_name`, `users`.`code` AS `user_code`" +
		" FROM `submissions`" +
		" JOIN `users` ON `users`.`id` = `submissions`.`user_id`" +
		" WHERE `class_id` = ?"
	if err := tx.Select(&submissions, query, classID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if _, err := tx.Exec("UPDATE `classes` SET `submission_closed` = true WHERE `id` = ?", classID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	zipFilePath := AssignmentsDirectory + classID + ".zip"
	if err := createSubmissionsZip2(zipFilePath, classID, submissions); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	c.Response().Header().Set("x-accel-redirect", "/assignments/"+classID+".zip")
	return c.NoContent(http.StatusOK)
}

func createSubmissionsZip2(zipFilePath string, classID string, submissions []Submission) error {
	tmpfile, err := os.Create(zipFilePath)
	if err != nil {
		return err
	}
	w := zip.NewWriter(tmpfile)
	for _, submission := range submissions {
		header := &zip.FileHeader{
			Name:   submission.UserCode + "-" + submission.FileName,
			Method: zip.Store,
		}
		f, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		src, err := os.Open(AssignmentsDirectory + classID + "-" + submission.UserID + ".pdf")
		if err != nil {
			return err
		}
		io.Copy(f, src)
		src.Close()
	}
	w.Close()
	tmpfile.Close()
	return nil
}

// ---------- Announcement API ----------

type AnnouncementWithoutDetail struct {
	ID         string `json:"id" db:"id"`
	CourseID   string `json:"course_id" db:"course_id"`
	CourseName string `json:"course_name" db:"course_name"`
	Title      string `json:"title" db:"title"`
	Unread     bool   `json:"unread" db:"unread"`
}

type GetAnnouncementsResponse struct {
	UnreadCount   int                         `json:"unread_count"`
	Announcements []AnnouncementWithoutDetail `json:"announcements"`
}

// GetAnnouncementList GET /api/announcements ????????????????????????
func (h *handlers) GetAnnouncementList(c echo.Context) error {
	userID, _, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var announcements []AnnouncementWithoutDetail
	var args []interface{}
	query := "SELECT `announcements`.`id`, `announcements`.`course_id` AS `course_id`, `announcements`.`course_name`, `announcements`.`title`, false AS `unread`" +
		" FROM `announcements`" +
		" WHERE 1=1"

	if courseID := c.QueryParam("course_id"); courseID != "" {
		query += " AND `announcements`.`course_id` = ?"
		args = append(args, courseID)
	} else {
		var courseIDs []string
		if err := h.DB.Select(&courseIDs, "SELECT course_id FROM `registrations` WHERE `user_id` = ?", userID); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		if len(courseIDs) == 0 {
			query += " AND 1=0"
		} else {
			wq, wqargs, err := sqlx.In(" AND `announcements`.`course_id` IN (?)", courseIDs)
			if err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}
			query += wq
			args = append(args, wqargs...)
		}
	}

	query += " ORDER BY `announcements`.`id` DESC LIMIT ? OFFSET ?"
	var page int
	if c.QueryParam("page") == "" {
		page = 1
	} else {
		page, err = strconv.Atoi(c.QueryParam("page"))
		if err != nil || page <= 0 {
			return c.String(http.StatusBadRequest, "Invalid page.")
		}
	}
	limit := 20
	offset := limit * (page - 1)
	// limit??????????????????????????????????????????limit?????????????????????????????????????????????????????????????????????????????????
	args = append(args, limit+1, offset)

	if err := h.DB.Select(&announcements, query, args...); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var unreadCount int
	//if err := h.DB.Get(&unreadCount, "SELECT COUNT(*) FROM `unread_announcements` WHERE `user_id` = ? AND NOT `is_deleted`", userID); err != nil {
	//	c.Logger().Error(err)
	//	return c.NoContent(http.StatusInternalServerError)
	//}
	if res, err := h.Redis.SCard(context.TODO(), "unread_announcements:"+userID).Result(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else {
		unreadCount = int(res)
	}

	newAnnouncements := make([]AnnouncementWithoutDetail, 0, len(announcements))
	if unreadCount > 0 && len(announcements) > 0 {
		//announcementIDs := make([]string, 0, len(announcements))
		//for _, announcement := range announcements {
		//	announcementIDs = append(announcementIDs, announcement.ID)
		//}
		//query, args, err = sqlx.In("SELECT announcement_id FROM `unread_announcements` WHERE `user_id` = ? AND NOT `is_deleted` AND announcement_id IN (?)", userID, announcementIDs)
		//if err != nil {
		//	c.Logger().Error(err)
		//	return c.NoContent(http.StatusInternalServerError)
		//}
		//var unreadAnnouncementIDs []string
		//if err := h.DB.Select(&unreadAnnouncementIDs, query, args...); err != nil {
		//	if err != sql.ErrNoRows {
		//		c.Logger().Error(err)
		//		return c.NoContent(http.StatusInternalServerError)
		//	}
		//}
		//unreadMap := make(map[string]struct{}, len(unreadAnnouncementIDs))
		//if len(unreadAnnouncementIDs) > 0 {
		//	for _, unreadAnnouncementID := range unreadAnnouncementIDs {
		//		unreadMap[unreadAnnouncementID] = struct{}{}
		//	}
		//}
		for _, announcement := range announcements {
			//if _, ok := unreadMap[announcement.ID]; ok {
			//	announcement.Unread = true
			//}
			var res bool
			if res, err = h.Redis.SIsMember(context.TODO(), "unread_announcements:"+userID, announcement.ID).Result(); err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}
			announcement.Unread = res
			newAnnouncements = append(newAnnouncements, announcement)
		}
		announcements = newAnnouncements
	}

	var links []string
	linkURL, err := url.Parse(c.Request().URL.Path + "?" + c.Request().URL.RawQuery)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	q := linkURL.Query()
	if page > 1 {
		q.Set("page", strconv.Itoa(page-1))
		linkURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf("<%v>; rel=\"prev\"", linkURL))
	}
	if len(announcements) > limit {
		q.Set("page", strconv.Itoa(page+1))
		linkURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf("<%v>; rel=\"next\"", linkURL))
	}
	if len(links) > 0 {
		c.Response().Header().Set("Link", strings.Join(links, ","))
	}

	if len(announcements) == limit+1 {
		announcements = announcements[:len(announcements)-1]
	}

	// ???????????????????????????????????????0??????????????????????????????
	announcementsRes := append(make([]AnnouncementWithoutDetail, 0, len(announcements)), announcements...)

	return c.JSON(http.StatusOK, GetAnnouncementsResponse{
		UnreadCount:   unreadCount,
		Announcements: announcementsRes,
	})
}

type Announcement struct {
	ID       string `db:"id"`
	CourseID string `db:"course_id"`
	Title    string `db:"title"`
	Message  string `db:"message"`
}

type AddAnnouncementRequest struct {
	ID       string `json:"id"`
	CourseID string `json:"course_id"`
	Title    string `json:"title"`
	Message  string `json:"message"`
}

// AddAnnouncement POST /api/announcements ????????????????????????
func (h *handlers) AddAnnouncement(c echo.Context) error {
	var req AddAnnouncementRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var courseName string
	if err := tx.Get(&courseName, "SELECT name FROM `courses` WHERE `id` = ?", req.CourseID); err != nil {
		if err == sql.ErrNoRows {
			return c.String(http.StatusNotFound, "No such course.")
		}
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if _, err := tx.Exec("INSERT INTO `announcements` (`id`, `course_id`, `course_name`, `title`, `message`) VALUES (?, ?, ?, ?, ?)",
		req.ID, req.CourseID, courseName, req.Title, req.Message); err != nil {
		_ = tx.Rollback()
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == uint16(mysqlErrNumDuplicateEntry) {
			var announcement Announcement
			if err := h.DB.Get(&announcement, "SELECT * FROM `announcements` WHERE `id` = ?", req.ID); err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}
			if announcement.CourseID != req.CourseID || announcement.Title != req.Title || announcement.Message != req.Message {
				return c.String(http.StatusConflict, "An announcement with the same id already exists.")
			}
			return c.NoContent(http.StatusCreated)
		}
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	userIDs, err := h.Redis.SMembers(context.TODO(), "registrations:"+req.CourseID).Result()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	for _, userID := range userIDs {
		if err := h.Redis.SAdd(context.TODO(), "unread_announcements:"+userID, req.ID).Err(); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
	}

	//query := "INSERT INTO `unread_announcements` (`announcement_id`, `user_id`)" +
	//	" SELECT ?, `registrations`.`user_id` FROM `registrations`" +
	//	" WHERE `registrations`.`course_id` = ?"
	//if _, err := tx.Exec(query, req.ID, req.CourseID); err != nil {
	//	c.Logger().Error(err)
	//	return c.NoContent(http.StatusInternalServerError)
	//}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusCreated)
}

type AnnouncementDetail struct {
	ID         string `json:"id" db:"id"`
	CourseID   string `json:"course_id" db:"course_id"`
	CourseName string `json:"course_name" db:"course_name"`
	Title      string `json:"title" db:"title"`
	Message    string `json:"message" db:"message"`
	Unread     bool   `json:"unread" db:"unread"`
}

var annoucementsMap = sync.Map{} // map[string]AnnouncementDetail{}

// GetAnnouncementDetail GET /api/announcements/:announcementID ????????????????????????
func (h *handlers) GetAnnouncementDetail(c echo.Context) error {
	userID, _, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	announcementID := c.Param("announcementID")

	var unread bool
	//var result sql.Result
	//if result, err = h.DB.Exec("UPDATE `unread_announcements` SET `is_deleted` = true WHERE `announcement_id` = ? AND `user_id` = ?", announcementID, userID); err != nil {
	//	c.Logger().Error(err)
	//	return c.NoContent(http.StatusInternalServerError)
	//}
	//if cnt, _ := result.RowsAffected(); cnt == 1 {
	//	unread = true
	//}
	var unreadCount int64
	if unreadCount, err = h.Redis.SRem(context.TODO(), "unread_announcements:"+userID, announcementID).Result(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if unreadCount == 1 {
		unread = true
	}

	var announcement AnnouncementDetail
	if _ann, ok := annoucementsMap.Load(announcementID); ok {
		announcement = _ann.(AnnouncementDetail)
	} else {
		query := "SELECT `announcements`.`id`, `announcements`.`course_id` AS `course_id`, `announcements`.`course_name`, `announcements`.`title`, `announcements`.`message`, true AS `unread`" +
			" FROM `announcements`" +
			" WHERE `announcements`.`id` = ?"
		if err := h.DB.Get(&announcement, query, announcementID); err != nil && err != sql.ErrNoRows {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		} else if err == sql.ErrNoRows {
			return c.String(http.StatusNotFound, "No such announcement.")
		}
		annoucementsMap.Store(announcementID, announcement)
	}
	announcement.Unread = unread

	var registrationCount int
	if err := h.DB.Get(&registrationCount, "SELECT COUNT(*) FROM `registrations` WHERE `course_id` = ? AND `user_id` = ?", announcement.CourseID, userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if registrationCount == 0 {
		return c.String(http.StatusNotFound, "No such announcement.")
	}

	return c.JSON(http.StatusOK, announcement)
}
