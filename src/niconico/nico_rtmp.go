package niconico

import (
	"fmt"
	"encoding/xml"
	"io/ioutil"
	"regexp"
	"strings"
	"net/url"
	"sync"
	"log"
	"net/http"

	"../rtmp"
	"../amf"
	"../options"
	"../file"
)

type Content struct {
	Id string `xml:"id,attr"`
	Text string `xml:",chardata"`
}
type Tickets struct {
	Name string `xml:"name,attr"`
	Text string `xml:",chardata"`
}
type Status struct {
	Title                   string  `xml:"stream>title"`
	CommunityId             string  `xml:"stream>default_community"`
	Id                      string  `xml:"stream>id"`
	Provider                string  `xml:"stream>provider_type"`
	IsArchive               bool    `xml:"stream>archive"`
	IsArchivePlayerServer   bool    `xml:"stream>is_archiveplayserver"`
	Ques                  []string  `xml:"stream>quesheet>que"`
	Contents              []Content `xml:"stream>contents_list>contents"`
	IsPremium               bool    `xml:"user>is_premium"`
	Url                     string  `xml:"rtmp>url"`
	Ticket                  string  `xml:"rtmp>ticket"`
	Tickets               []Tickets `xml:"tickets>stream"`
	ErrorCode               string  `xml:"error>code"`
	streams               []Stream
	chStream                chan bool
	wg                     *sync.WaitGroup
}
type Stream struct {
	originUrl string
	streamName string
	originTicket string
}
func (status *Status) quesheet() {
	stream := make(map[string][]Stream)
	playType := make(map[string]string)

	// timeshift; <quesheet> tag
	re_pub := regexp.MustCompile(`\A/publish\s+(\S+)\s+(?:(\S+?),)?(\S+?)(?:\?(\S+))?\z`)
	re_play := regexp.MustCompile(`\A/play\s+(\S+)\s+(\S+)\z`)

	for _, q := range status.Ques {
		// /publish lv* /content/*/lv*_*_1_*.f4v
		if ma := re_pub.FindStringSubmatch(q); len(ma) >= 5 {
			stream[ma[1]] = append(stream[ma[1]], Stream{
				originUrl: ma[2],
				streamName: ma[3],
				originTicket: ma[4],
			})

		// /play ...
		} else if ma := re_play.FindStringSubmatch(q); len(ma) > 0 {
			// /play case:sp:rtmp:lv*_s_lv*,mobile:rtmp:lv*_s_lv*_sub1,premium:rtmp:lv*_s_lv*_sub1,default:rtmp:lv*_s_lv* main
			if strings.HasPrefix(ma[1], "case:") {
				s0 := ma[1]
				s0 = strings.TrimPrefix(s0, "case:")
				cases := strings.Split(s0, ",")
				// sp:rtmp:lv*_s_lv*
				re := regexp.MustCompile(`\A(\S+?):rtmp:(\S+?)\z`)
				for _, c := range cases {
					if ma := re.FindStringSubmatch(c); len(ma) > 0 {
						playType[ma[1]] = ma[2]
					}
				}

			// /play rtmp:lv* main
			} else {
				re := regexp.MustCompile(`\Artmp:(\S+?)\z`)
				if ma := re.FindStringSubmatch(ma[1]); len(ma) > 0 {
					playType["default"] = ma[1]
				}
			}
		}
	}

	pt, ok := playType["premium"]
	if ok && status.IsPremium {
		s, ok := stream[ pt ]
		if ok {
			status.streams = s
		}
	} else {
		pt, ok := playType["default"]
		if ok {
			s, ok := stream[ pt ]
			if ok {
				status.streams = s
			}
		}
	}
}
func (status *Status) initStreams() {

	if len(status.streams) > 0 {
		return
	}

	//if status.isOfficialLive() {
		status.contentsOfficialLive()
	//} else if status.isLive() {
		status.contentsNonOfficialLive()
	//} else {
		status.quesheet()
	//}

	return
}
func (status *Status) getFileName(index int) (name string) {
	if len(status.streams) == 1 {
		//name = fmt.Sprintf("%s.flv", status.Id)
		name = fmt.Sprintf("%s-%s-%s.flv", status.Id, status.CommunityId, status.Title)
	} else if len(status.streams) > 1 {
		//name = fmt.Sprintf("%s-%d.flv", status.Id, 1 + index)
		name = fmt.Sprintf("%s-%s-%s-%d.flv", status.Id, status.CommunityId, status.Title, 1 + index)
	} else {
		log.Fatalf("No stream")
	}
	return
}
func (status *Status) contentsNonOfficialLive() {
	re := regexp.MustCompile(`\A(?:rtmp:)?(rtmp\w*://\S+?)(?:,(\S+?)(?:\?(\S+))?)?\z`)

	// Live (not timeshift); <contents_list> tag
	for _, c := range status.Contents {
		if ma := re.FindStringSubmatch(c.Text); len(ma) > 0 {
			status.streams = append(status.streams, Stream{
				originUrl: ma[1],
				streamName: ma[2],
				originTicket: ma[3],
			})
		}
	}

}
func (status *Status) contentsOfficialLive() {

	tickets := make(map[string] string)
	for _, t := range status.Tickets {
		tickets[t.Name] = t.Text
	}

	for _, c := range status.Contents {
		if strings.HasPrefix(c.Text, "case:") {
			c.Text = strings.TrimPrefix(c.Text, "case:")

			for _, c := range strings.Split(c.Text, ",") {
				c, e := url.PathUnescape(c)
				if e != nil {
					fmt.Printf("%v\n", e)
				}

				re := regexp.MustCompile(`\A(\S+?):(?:limelight:|akamai:)?(\S+),(\S+)\z`)
				if ma := re.FindStringSubmatch(c); len(ma) > 0 {
					fmt.Printf("\n%#v\n", ma)
					switch ma[1] {
						default:
							fmt.Printf("unknown contents case %#v\n", ma[1])
						case "mobile":
						case "middle":
						case "default":
							status.Url = ma[2]
							t, ok := tickets[ma[3]]
							if (! ok) {
								fmt.Printf("not found %s\n", ma[3])
							}
							fmt.Printf("%s\n", t)
							status.streams = append(status.streams, Stream{
								streamName: ma[3],
								originTicket: t,
							})
					}
				}
			}
		}
	}
}

func (status *Status) relayStreamName(i, offset int) (s string) {
	s = regexp.MustCompile(`[^/\\]+\z`).FindString(status.streams[i].streamName)
	if offset >= 0 {
		s += fmt.Sprintf("_%d", offset)
	}
	return
}

func (status *Status) streamName(i, offset int) (name string, err error) {
	if status.isOfficialLive() {
		if i >= len(status.streams) {
			err = fmt.Errorf("(status *Status) streamName(i int): Out of index: %d\n", i)
			return
		}

		name = status.streams[i].streamName
		if status.streams[i].originTicket != "" {
			name += "?" + status.streams[i].originTicket
		}
		return

	} else if status.isOfficialTs() {
		name = status.streams[i].streamName
		name = regexp.MustCompile(`(?i:\.flv)$`).ReplaceAllString(name, "")
		if regexp.MustCompile(`(?i:\.(?:f4v|mp4))$`).MatchString(name) {
			name = "mp4:" + name
		} else if regexp.MustCompile(`(?i:\.raw)$`).MatchString(name) {
			name = "raw:" + name
		}

	} else {
		name = status.relayStreamName(i, offset)
	}

	return
}
func (status *Status) tcUrl() (url string, err error) {
	if status.Url != "" {
		url = status.Url
		return
	} else {
		status.contentsOfficialLive()
	}

	if status.Url != "" {
		url = status.Url
		return
	}

	err = fmt.Errorf("tcUrl not found")
	return
}
func (status *Status) isTs() bool {
	return status.IsArchive
}
func (status *Status) isLive() bool {
	return (! status.IsArchive)
}
func (status *Status) isOfficialLive() bool {
	return (status.Provider == "official") && (! status.IsArchive)
}
func (status *Status) isOfficialTs() bool {
	return (status.IsArchive && status.Provider == "official") ||
	(status.IsArchivePlayerServer && status.Provider == "channel")
}

func (st Stream) relayStreamName(offset int) (s string) {
	s = regexp.MustCompile(`[^/\\]+\z`).FindString(st.streamName)
	if offset >= 0 {
		s += fmt.Sprintf("_%d", offset)
	}
	return
}
func (st Stream) noticeStreamName(offset int) (s string) {
	s = st.streamName
	s = regexp.MustCompile(`(?i:\.flv)$`).ReplaceAllString(s, "")
	if regexp.MustCompile(`(?i:\.(?:f4v|mp4))$`).MatchString(s) {
		s = "mp4:" + s
	} else if regexp.MustCompile(`(?i:\.raw)$`).MatchString(s) {
		s = "raw:" + s
	}

	if st.originTicket != "" {
		s += "?" + st.originTicket
	}

	return
}

func (status *Status) recStream(index int) (err error) {
	defer func(){
		<-status.chStream
		status.wg.Done()
	}()

	stream := status.streams[index]

	tcUrl, e := status.tcUrl()
	if e != nil {
		err = e
		return
	}

	rtmpConn, e := rtmp.NewRtmp(
		// tcUrl
		tcUrl,
		// swfUrl
		"http://live.nicovideo.jp/nicoliveplayer.swf?180116154229",
		// pageUrl
		"http://live.nicovideo.jp/watch/" + status.Id,
		// option
		status.Ticket,
	)
	if e != nil {
		fmt.Printf("%v\n", e)
		return
	}

	fileName := status.getFileName(index)
	if fileName, err = file.GetFileNameNext(fileName); err != nil {
		return
	}
	rtmpConn.SetFlvName(fileName)

// [FIXME] goto
RETRY:
	// default: 2500000
	if err = rtmpConn.SetPeerBandwidth(100*1000*1000, 0); err != nil {
		fmt.Printf("SetPeerBandwidth: %v\n", err)
		return
	}

	if err = rtmpConn.WindowAckSize(2500000); err != nil {
		fmt.Printf("WindowAckSize: %v\n", err)
		return
	}

	if err = rtmpConn.CreateStream(); err != nil {
		fmt.Printf("CreateStream %v\n", err)
		return
	}

	if err = rtmpConn.SetBufferLength(0, 2000); err != nil {
		fmt.Printf("SetBufferLength: %v\n", err)
		return
	}

	var offset int
	if status.IsArchive {
		offset = 0
	} else {
		offset = -2
	}
	if status.isOfficialTs() {
		err = rtmpConn.Command(
			"sendFileRequest", []interface{} {
			nil,
			amf.SwitchToAmf3(),
			[]string{
				stream.streamName,
			},
		})
		if err != nil {
			return
		}
	} else if (! status.isOfficialLive()) {
		// /publishの第二引数
		// streamName(param1:String)
		// 「,」で区切る
		// ._originUrl, streamName(playStreamName)
		// streamName に、「?」がついてるなら originTickt となる
		// streamName の.flvは削除する
		// streamNameが/\.(f4v|mp4)$/iなら、頭にmp4:をつける
		// /\.raw$/iなら、raw:をつける。
		// relayStreamName: streamNameの頭からスラッシュまでを削除したもの

		err = rtmpConn.Command(
			"nlPlayNotice", []interface{} {
			nil,
			// _connection.request.originUrl
			stream.originUrl,

			// this._connection.request.playStreamRequest
			// originticket あるなら
			// playStreamName ? this._originTicket
			// 無いなら playStreamName
			stream.noticeStreamName(offset),

			// var _loc1_:String = this._relayStreamName;
			// if(this._offset != -2)
			// {
			// _loc1_ = _loc1_ + ("_" + this.offset);
			// }
			// user nama: String 'lvxxxxxxxxx'
			// user kako: lvxxxxxxxxx_xxxxxxxxxxxx_1_xxxxxx.f4v_0
			stream.relayStreamName(offset),

			// seek offset
			// user nama: -2, user kako: 0
			offset,
		})
		if err != nil {
			fmt.Printf("nlPlayNotice %v\n", err)
			return
		}
	}

	if err = rtmpConn.SetBufferLength(1, 3600 * 1000); err != nil {
		fmt.Printf("SetBufferLength: %v\n", err)
		return
	}

	rtmpConn.SetFixAggrTimestamp(true)

	fmt.Printf("debug start play\n")

	// user kako: lv*********_************_*_******.f4v_0
	// official or channel ts: mp4:/content/********/lv*********_************_*_******.f4v
	//if err = rtmpConn.Play(status.origin.playStreamName(status.isTsOfficial(), offset)); err != nil {
	streamName, e := status.streamName(index, offset)
	if e != nil {
		err = e
		return
	}

	if status.isOfficialTs() {
		ts := rtmpConn.GetTimestamp()
		if ts > 1000 {
			err = rtmpConn.PlayTime(streamName, ts - 1000)
		} else {
			err = rtmpConn.PlayTime(streamName, -5000)
		}

	} else if status.isTs() {
		err = rtmpConn.PlayTime(streamName, -5000)

	} else {
		err = rtmpConn.Play(streamName)
	}
	if err != nil {
		fmt.Printf("Play: %v\n", err)
		return
	}
	// Non-recordedな過去録でseekしても、timestampが変わるだけで
	// 最初からの再生となってしまうのでやらないこと

	_, incomplete, err := rtmpConn.Wait()
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}
	if incomplete && status.isOfficialTs() {
		rtmpConn.Connect()
		goto RETRY
	}
	fmt.Printf("done\n")
	return
}

func (status *Status) recAllStreams(opt options.Option) (err error) {

	status.initStreams()

	var MaxConn int
	if opt.NicoRtmpMaxConn == 0 {
		if status.isOfficialTs() {
			MaxConn = 1
		} else {
			MaxConn = 4
		}
	} else if opt.NicoRtmpMaxConn < 0 {
		MaxConn = 1
	} else {
		MaxConn = opt.NicoRtmpMaxConn
	}

	status.wg = &sync.WaitGroup{}
	status.chStream = make(chan bool, MaxConn)

	for index, _ := range status.streams {
		if opt.NicoRtmpIndex != nil {
			if tes, ok := opt.NicoRtmpIndex[index]; !ok || !tes {
				continue
			}
		}

		status.chStream <- true
		status.wg.Add(1)

		go status.recStream(index)
	}

	status.wg.Wait()

	return
}

func getStatus(opt options.Option) (status *Status, notLogin bool, err error) {
	var url string

	// experimental
	if opt.NicoStatusHTTPS {
		url = fmt.Sprintf("https://ow.live.nicovideo.jp/api/getplayerstatus?v=%s", opt.NicoLiveId)
	} else {
		url = fmt.Sprintf("http://watch.live.nicovideo.jp/api/getplayerstatus?v=%s", opt.NicoLiveId)
	}

	req, _ := http.NewRequest("GET", url, nil)
	if opt.NicoSession != "" {
		req.Header.Set("Cookie", "user_session=" + opt.NicoSession)
	}

	// experimental
	if opt.NicoStatusHTTPS {
		req.Header.Set("User-Agent", "Niconico/1.0 (Unix; U; iPhone OS 10.3.3; ja-jp; nicoiphone; iPhone5,2) Version/6.65")
	}

	client := new(http.Client)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	dat, _ := ioutil.ReadAll(resp.Body)

	status = &Status{}
	err = xml.Unmarshal(dat, status)
	if err != nil {
		fmt.Println(string(dat))
		fmt.Printf("error: %v", err)
		return
	}

	switch status.ErrorCode {
	case "":
	case "notlogin":
		notLogin = true
	default:
		err = fmt.Errorf("Error code: %s\n", status.ErrorCode)
		return
	}

	return
}

func NicoRecRtmp(opt options.Option) (notLogin bool, err error) {
	status, notLogin, err := getStatus(opt)
	if err != nil {
		return
	}
	if notLogin {
		return
	}

	status.recAllStreams(opt)
	return
}