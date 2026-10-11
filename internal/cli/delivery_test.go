package cli

import (
 "bytes"
 "encoding/json"
 "net/http/httptest"
 "os"
 "path/filepath"
 "strings"
 "testing"
)

func TestDeliveryPublicCommitAssociation(t *testing.T) {
 root:=home(t); writeRun(t,root,"delivery-run","claude",early,"completed")
 t.Setenv("PATH", t.TempDir()) // A commit association must never invoke a provider.
 original,err:=os.ReadFile(filepath.Join(root,"delivery-run","manifest.json")); if err!=nil {t.Fatal(err)}
 var out, stderr bytes.Buffer
 if code:=Run([]string{"delivery","add","delivery-run","--commit",strings.Repeat("a",40)},&out,&stderr); code!=0 {t.Fatalf("delivery add exit=%d stderr=%s",code,&stderr)}
 out.Reset(); stderr.Reset()
 if code:=Run([]string{"delivery","show","delivery-run","--json"},&out,&stderr); code!=0 {t.Fatalf("delivery show exit=%d stderr=%s",code,&stderr)}
 var doc struct {SchemaVersion int `json:"schemaVersion"`; Associations []struct {Commit string `json:"commit"`} `json:"associations"`}
 if err:=json.Unmarshal(out.Bytes(),&doc);err!=nil {t.Fatal(err)}
 if doc.SchemaVersion!=1 || len(doc.Associations)!=1 || doc.Associations[0].Commit!=strings.Repeat("a",40) {t.Fatalf("wrong association %s",&out)}
 after,err:=os.ReadFile(filepath.Join(root,"delivery-run","manifest.json")); if err!=nil || !bytes.Equal(original,after) {t.Fatal("manifest changed")}
}

func TestDeliveryCapturedDetail(t *testing.T) {
 root:=home(t);writeRun(t,root,"delivery-run","claude",early,"completed")
 handler:=newViewHandler(root,"delivery-run",false);t.Cleanup(func(){_ = handler.Close()})
 read:=func() map[string]json.RawMessage {t.Helper();req:=httptest.NewRequest("GET","/api/runs/delivery-run",nil);req.Host="127.0.0.1:42817";res:=httptest.NewRecorder();handler.ServeHTTP(res,req);if res.Code!=200 {t.Fatalf("detail %d %s",res.Code,res.Body)};var doc map[string]json.RawMessage;if err:=json.Unmarshal(res.Body.Bytes(),&doc);err!=nil {t.Fatal(err)};return doc}
 if _,ok:=read()["delivery"];ok {t.Fatal("absent delivery must be omitted")}
 raw:=`{"schemaVersion":1,"associations":[{"commit":"`+strings.Repeat("a",40)+`","observedAt":"2026-10-11T00:00:00Z"}]}`
 if err:=os.WriteFile(filepath.Join(root,"delivery-run","delivery.json"),[]byte(raw),0600);err!=nil {t.Fatal(err)}
 if got:=read()["delivery"];!bytes.Contains(got,[]byte(strings.Repeat("a",40))) {t.Fatalf("captured detail delivery absent: %s",got)}
}
