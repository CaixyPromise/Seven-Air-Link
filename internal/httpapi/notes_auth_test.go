package httpapi
import("bufio";"bytes";"context";"io";"net/http";"strings";"testing";"time";"seven-air-link/internal/model")
func ticketSession(t *testing.T,r *rig,token string)(string,string){t.Helper();var ticket struct{Ticket string `json:"ticket"`};getJSON(t,r.raw(t,"POST","/api/v1/browser-tickets",token,map[string]any{},nil),&ticket);response:=r.raw(t,"POST","/api/v1/sessions","",map[string]string{"ticket":ticket.Ticket},nil);expect(t,response,200);var data struct{CSRF string `json:"csrf"`};getJSON(t,response,&data);return response.Cookies()[0].String(),data.CSRF}
func loginTestPassword(t *testing.T,r *rig,pw string)(string,string){t.Helper();response:=r.raw(t,"POST","/api/v1/sessions/password","",map[string]string{"password":pw},map[string]string{"Origin":r.http.URL});expect(t,response,200);if c:=response.Cookies()[0];!c.Secure||!c.HttpOnly||c.SameSite!=http.SameSiteStrictMode||c.MaxAge!=8*3600{t.Fatal("unsafe cookie",c)};var data struct{CSRF string `json:"csrf"`};getJSON(t,response,&data);return response.Cookies()[0].String(),data.CSRF}
func TestPasswordLoginChangeInvalidationAndIdentitySeparation(t *testing.T){
 r:=newRig(t);bot,_:=r.participant(t,"Unaffected Bot","bot","");dev:=r.join(t,"Unaffected Node");ownerCookie,_:=ticketSession(t,r,r.token);botCookie,_:=ticketSession(t,r,bot.Token);pw:="password login 中文 123";next:="new-password-login-456"
 expect(t,r.raw(t,"PUT","/api/v1/auth/password","",map[string]string{"password":pw},nil),401);for _,token:=range []string{bot.Token,dev.Token}{expect(t,r.raw(t,"PUT","/api/v1/auth/password",token,map[string]string{"password":pw},nil),403)}
 expect(t,r.raw(t,"PUT","/api/v1/auth/password",r.token,map[string]string{"password":"short"},nil),400);expect(t,r.raw(t,"PUT","/api/v1/auth/password",r.token,map[string]string{"password":pw},nil),200)
 expect(t,r.raw(t,"GET","/api/v1/me","",nil,map[string]string{"Cookie":ownerCookie}),401);expect(t,r.raw(t,"GET","/api/v1/me","",nil,map[string]string{"Cookie":botCookie}),200);expect(t,r.raw(t,"POST","/api/v1/sessions/password","",map[string]string{"password":"bad"},nil),403)
 cookie,csrf:=loginTestPassword(t,r,pw);expect(t,r.raw(t,"PUT","/api/v1/note","",map[string]any{"text":"CSRF","baseRevision":0},map[string]string{"Cookie":cookie}),403)
 expect(t,r.raw(t,"PUT","/api/v1/auth/password","",map[string]string{"password":next,"currentPassword":"bad"},map[string]string{"Cookie":cookie,"X-SLB-CSRF":csrf}),403)
 expect(t,r.raw(t,"PUT","/api/v1/auth/password","",map[string]string{"password":next,"currentPassword":pw},map[string]string{"Cookie":cookie,"X-SLB-CSRF":csrf}),200)
 expect(t,r.raw(t,"GET","/api/v1/me","",nil,map[string]string{"Cookie":cookie}),401);expect(t,r.raw(t,"GET","/api/v1/me","",nil,map[string]string{"Cookie":botCookie}),200)
 for _,token:=range []string{r.token,bot.Token,dev.Token}{expect(t,r.raw(t,"GET","/api/v1/me",token,nil,nil),200)};loginTestPassword(t,r,next);expect(t,r.raw(t,"POST","/api/v1/sessions/password","",map[string]string{"password":pw},nil),403)
 response:=r.raw(t,"GET","/api/v1/activity?limit=500",r.token,nil,nil);defer response.Body.Close();raw,_:=io.ReadAll(response.Body);if bytes.Contains(raw,[]byte(pw))||bytes.Contains(raw,[]byte(next))||bytes.Contains(raw,[]byte("pbkdf2")){t.Fatal("password data in audit")}
}
func TestPasswordRateLimitsOriginDisableAndTicketRecovery(t *testing.T){
 r:=newRig(t);pw:="password-rate-limit-123";expect(t,r.raw(t,"PUT","/api/v1/auth/password",r.token,map[string]string{"password":pw},nil),200)
 expect(t,r.raw(t,"POST","/api/v1/sessions/password","",map[string]string{"password":pw},map[string]string{"Origin":"https://attacker.invalid"}),403)
 for i:=0;i<5;i++{expect(t,r.raw(t,"POST","/api/v1/sessions/password","",map[string]string{"password":"incorrect"},nil),403)};expect(t,r.raw(t,"POST","/api/v1/sessions/password","",map[string]string{"password":pw},nil),429)
 cookie,csrf:=ticketSession(t,r,r.token);expect(t,r.raw(t,"DELETE","/api/v1/auth/password","",map[string]string{"currentPassword":pw},map[string]string{"Cookie":cookie,"X-SLB-CSRF":csrf}),200)
 var methods map[string]any;getJSON(t,r.raw(t,"GET","/api/v1/auth/methods","",nil,nil),&methods);if methods["password"]!=false||methods["ticket"]!=true{t.Fatal(methods)};ticketSession(t,r,r.token)
}
func TestBrowserSessionListingRevocationIsScoped(t *testing.T){
 r:=newRig(t);bot,_:=r.participant(t,"Browser Bot","bot","");a,csrf:=ticketSession(t,r,r.token);b,_:=ticketSession(t,r,r.token);botCookie,_:=ticketSession(t,r,bot.Token)
 var list struct{Sessions []sessionView `json:"sessions"`};getJSON(t,r.raw(t,"GET","/api/v1/sessions","",nil,map[string]string{"Cookie":a}),&list);if len(list.Sessions)!=2{t.Fatal(list)};other:="";for _,s:=range list.Sessions{if !s.Current{other=s.ID}};if other==""{t.Fatal("no other session")}
 expect(t,r.raw(t,"DELETE","/api/v1/sessions/"+other,bot.Token,nil,nil),404);expect(t,r.raw(t,"DELETE","/api/v1/sessions/"+other,"",nil,map[string]string{"Cookie":a,"X-SLB-CSRF":csrf}),200);expect(t,r.raw(t,"GET","/api/v1/me","",nil,map[string]string{"Cookie":b}),401);expect(t,r.raw(t,"GET","/api/v1/me","",nil,map[string]string{"Cookie":a}),200)
 expect(t,r.raw(t,"DELETE","/api/v1/sessions/all","",nil,map[string]string{"Cookie":a,"X-SLB-CSRF":csrf}),200);expect(t,r.raw(t,"GET","/api/v1/me","",nil,map[string]string{"Cookie":a}),401);expect(t,r.raw(t,"GET","/api/v1/me","",nil,map[string]string{"Cookie":botCookie}),200)
}
func TestNoteAPIRevisionConflictPrivacyAndValidation(t *testing.T){
 r:=newRig(t);bot,_:=r.participant(t,"Note Bot","bot","");dev:=r.join(t,"Node");expect(t,r.raw(t,"GET","/api/v1/note","",nil,nil),401);expect(t,r.raw(t,"GET","/api/v1/note",dev.Token,nil,nil),403)
 text:="<script>window.shouldNotRun=1</script> 中文\n  SQL\n";response:=r.raw(t,"PUT","/api/v1/note",bot.Token,map[string]any{"text":text,"baseRevision":0,"correlationId":"note-test"},nil);expect(t,response,200);var n model.SharedNote;getJSON(t,response,&n);if n.Text!=text||n.Revision!=1||n.UpdatedBy!=bot.SubjectID{t.Fatal(n)}
 response=r.raw(t,"PUT","/api/v1/note",r.token,map[string]any{"text":"lost update","baseRevision":0},nil);expect(t,response,409);var conflict struct{Error model.APIError `json:"error"`;Note model.SharedNote `json:"note"`};getJSON(t,response,&conflict);if conflict.Note.Text!=text||conflict.Error.Code!="note_revision_conflict"{t.Fatal(conflict)}
 for _,q:=range []map[string]any{{"text":"blind"},{"text":"negative","baseRevision":-1},{"text":strings.Repeat("a",65537),"baseRevision":1}}{expect(t,r.raw(t,"PUT","/api/v1/note",r.token,q,nil),400)}
 response=r.raw(t,"GET","/api/v1/activity?limit=500",r.token,nil,nil);defer response.Body.Close();body,_:=io.ReadAll(response.Body);if bytes.Contains(body,[]byte(text))||!bytes.Contains(body,[]byte("note.updated")){t.Fatal("bad note audit")};for _,path:=range []string{"/notes.js","/auth.js"}{expect(t,r.raw(t,"GET",path,"",nil,nil),200)}
}
func TestNoteSSERealTransportAndRevocation(t *testing.T){
 r:=newRig(t);bot,bc:=r.participant(t,"Note Subscriber","bot","");ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel();response,e:=bc.Request(ctx,"GET","/api/v1/note/events",nil);if e!=nil{t.Fatal(e)};defer response.Body.Close();expect(t,response,200)
 lines:=make(chan string,64);go func(){defer close(lines);scan:=bufio.NewScanner(response.Body);for scan.Scan(){select{case lines<-scan.Text():case<-ctx.Done():return}}}()
 await:=func(want string){t.Helper();for{select{case line,ok:=<-lines:if !ok{t.Fatalf("stream ended before %s",want)};if strings.Contains(line,want){return};case<-ctx.Done():t.Fatalf("did not receive %s",want)}}}
 await("note.snapshot");expect(t,r.raw(t,"PUT","/api/v1/note",r.token,map[string]any{"text":"live update","baseRevision":0},nil),200);await("note.updated");await("live update");expect(t,r.raw(t,"DELETE","/api/v1/principals/"+bot.SubjectID,r.token,nil,nil),200);await("session.expired")
}
