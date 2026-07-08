package usage

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Row stats 表格的一行
type Row struct {
	Project       string
	Model         string
	Date          string
	Input         int64
	Output        int64
	CacheCreation int64
	CacheRead     int64
	Total         int64
}

// parseSince 把 --since 参数转成起始日期 "YYYY-MM-DD"。
// "1d"/"7d"/"30d" → 今天往前 N 天；"YYYY-MM-DD" → 原样；空 → 默认 7 天。
func parseSince(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().AddDate(0, 0, -7).Format("2006-01-02"), nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 0 {
			return "", fmt.Errorf("无效的 --since 区间 %q（应为 1d/7d/30d 或 YYYY-MM-DD）", s)
		}
		return time.Now().AddDate(0, 0, -n).Format("2006-01-02"), nil
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return "", fmt.Errorf("无效的 --since 日期 %q（应为 1d/7d/30d 或 YYYY-MM-DD）", s)
	}
	return s, nil
}

// Query 按过滤条件从 File 中选出 Row 列表，按 project/model/date 排序。
// project/model 为空表示不过滤；since 为起始日期 "YYYY-MM-DD"（含）。
func Query(f *File, project, model, since string) []Row {
	var rows []Row
	for proj, models := range f.Buckets {
		if project != "" && proj != project {
			continue
		}
		for mdl, dates := range models {
			if model != "" && mdl != model {
				continue
			}
			for date, u := range dates {
				if u == nil || date < since {
					continue
				}
				rows = append(rows, Row{
					Project: proj, Model: mdl, Date: date,
					Input:         u.Input,
					Output:        u.Output,
					CacheCreation: u.CacheCreation,
					CacheRead:     u.CacheRead,
					Total:         u.Input + u.Output + u.CacheCreation + u.CacheRead,
				})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Project != rows[j].Project {
			return rows[i].Project < rows[j].Project
		}
		if rows[i].Model != rows[j].Model {
			return rows[i].Model < rows[j].Model
		}
		return rows[i].Date < rows[j].Date
	})
	return rows
}

// Render 把 Row 列表渲染成表格字符串。
func Render(rows []Row) string {
	var b strings.Builder
	if len(rows) == 0 {
		return "（暂无用量数据）\n"
	}
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tMODEL\tDATE\tINPUT\tOUTPUT\tCACHE_CREATE\tCACHE_READ\tTOTAL")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
			r.Project, r.Model, r.Date, r.Input, r.Output, r.CacheCreation, r.CacheRead, r.Total)
	}
	tw.Flush()
	return b.String()
}

// RunStats 供 ccp stats 命令调用：加载文件 → 过滤 → 渲染。
// 文件不存在返回空数据提示（不报错）；损坏返回错误。
func RunStats(path, project, since, model string) (string, error) {
	sinceDate, err := parseSince(since)
	if err != nil {
		return "", err
	}
	f, err := LoadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "（暂无用量数据）\n", nil
		}
		return "", err
	}
	return Render(Query(f, project, model, sinceDate)), nil
}
