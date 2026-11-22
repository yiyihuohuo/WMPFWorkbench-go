// Own-host-only observer. Frida core is linked in this isolated worker process.
// The controller assigns us to its job before creating gate_file. We also assign
// the suspended host to a KILL_ON_JOB_CLOSE job before its first resume.
#include "frida-core.h"
#include <windows.h>
#include <tlhelp32.h>
#include <algorithm>
#include <atomic>
#include <cstdio>
#include <filesystem>
#include <fstream>
#include <map>
#include <mutex>
#include <queue>
#include <string>
#include <thread>
#include <vector>
#include "agents.h"

namespace fs=std::filesystem;
static std::wstring wide(const std::string&s){int n=MultiByteToWideChar(CP_UTF8,MB_ERR_INVALID_CHARS,s.data(),(int)s.size(),nullptr,0);std::wstring w(n,0);if(n)MultiByteToWideChar(CP_UTF8,0,s.data(),(int)s.size(),w.data(),n);return w;}
static std::string utf8(const std::wstring&s){int n=WideCharToMultiByte(CP_UTF8,0,s.data(),(int)s.size(),nullptr,0,nullptr,nullptr);std::string out(n,0);if(n)WideCharToMultiByte(CP_UTF8,0,s.data(),(int)s.size(),out.data(),n,nullptr,nullptr);return out;}
static std::string quote(const std::string&s){JsonNode*n=json_node_new(JSON_NODE_VALUE);json_node_set_string(n,s.c_str());gchar*raw=json_to_string(n,FALSE);std::string out(raw);g_free(raw);json_node_free(n);return out;}
struct Config{std::string appid,host,runtime,profile,inputs,out,stop,gate;int seconds=0;bool hide=true,fixture=false;};
struct Attachment{FridaSession*session=nullptr;FridaScript*script=nullptr;};
struct Observer{
 Config cfg;FridaDeviceManager*manager=nullptr;FridaDevice*device=nullptr;HANDLE job=nullptr,login=nullptr;guint hostPID=0;std::map<DWORD,HANDLE>owned;std::map<DWORD,Attachment>sessions;std::mutex queueMutex,logMutex;std::queue<FridaChild*>children;std::atomic<bool>loginFailed{false};std::ofstream events;std::string source;std::map<guint,std::string>output;ULONGLONG began=GetTickCount64();
 void log(const char*event,DWORD pid=0,const std::string&payload=""){std::lock_guard<std::mutex>lock(logMutex);std::string row="{\"event\":"+quote(event)+",\"elapsed\":"+std::to_string((GetTickCount64()-began)/1000.0)+",\"pid\":"+std::to_string(pid);if(!payload.empty())row+=",\"payload\":"+payload;row+="}";printf("%s\n",row.c_str());fflush(stdout);if(events){events<<row<<"\n";events.flush();}}
 bool remember(DWORD pid){if(owned.count(pid))return true;HANDLE h=OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION|SYNCHRONIZE|PROCESS_TERMINATE,FALSE,pid);if(!h)return false;owned[pid]=h;log("process.seen",pid);return true;}
 bool alive(DWORD pid){auto i=owned.find(pid);return i!=owned.end()&&WaitForSingleObject(i->second,0)==WAIT_TIMEOUT;}
 bool attach(DWORD pid){GError*err=nullptr;Attachment a;a.session=frida_device_attach_sync(device,pid,nullptr,nullptr,&err);if(!a.session){if(err)g_error_free(err);log("session.attach_error",pid);return false;}auto options=frida_script_options_new();frida_script_options_set_name(options,"wmpf-owned-observer");frida_script_options_set_runtime(options,FRIDA_SCRIPT_RUNTIME_QJS);a.script=frida_session_create_script_sync(a.session,source.c_str(),options,nullptr,&err);g_object_unref(options);if(!a.script){if(err)g_error_free(err);frida_session_detach_sync(a.session,nullptr,nullptr);g_object_unref(a.session);log("script.create_error",pid);return false;}g_signal_connect(a.script,"message",G_CALLBACK(message),this);frida_script_load_sync(a.script,nullptr,&err);if(err){g_error_free(err);g_object_unref(a.script);frida_session_detach_sync(a.session,nullptr,nullptr);g_object_unref(a.session);log("script.load_error",pid);return false;}sessions[pid]=a;log("session.attached",pid);return true;}
 static void message(FridaScript*,const gchar*raw,GBytes*,gpointer data){auto*self=(Observer*)data;JsonParser*p=json_parser_new();if(!json_parser_load_from_data(p,raw,-1,nullptr)){g_object_unref(p);return;}JsonNode*root=json_parser_get_root(p);if(!JSON_NODE_HOLDS_OBJECT(root)){g_object_unref(p);return;}auto obj=json_node_get_object(root);const char*type=json_object_get_string_member_with_default(obj,"type","");if(!strcmp(type,"send")){JsonNode*payload=json_object_get_member(obj,"payload");if(payload&&JSON_NODE_HOLDS_OBJECT(payload)){auto o=json_node_get_object(payload);const char*event=json_object_get_string_member_with_default(o,"event","");DWORD pid=(DWORD)json_object_get_int_member_with_default(o,"pid",0);gchar*text=json_to_string(payload,FALSE);self->log("script",pid,text);g_free(text);if(!strcmp(event,"ilink.ThirdAppLogin.result.return")&&json_object_get_int_member_with_default(o,"logged_in",0)==1){if(self->login)SetEvent(self->login);self->log("login.confirmed",pid);}if(!strcmp(event,"ilink.ThirdAppLogin.result")&&json_object_get_int_member_with_default(o,"error_code",0)!=0)self->loginFailed=true;if(!strcmp(event,"observer_ready"))self->log("observer.ready",pid);}}else if(!strcmp(type,"error")){self->log("script.error");self->loginFailed=true;}g_object_unref(p);}
 static void childAdded(FridaDevice*,FridaChild*child,gpointer data){auto*self=(Observer*)data;g_object_ref(child);std::lock_guard<std::mutex>lock(self->queueMutex);self->children.push(child);}
 static void onOutput(FridaDevice*,guint pid,gint,GBytes*data,gpointer context){auto*self=(Observer*)context;gsize size=0;const char*bytes=(const char*)g_bytes_get_data(data,&size);auto&buffer=self->output[pid];buffer.append(bytes,size);size_t pos;while((pos=buffer.find('\n'))!=std::string::npos){auto line=buffer.substr(0,pos);buffer.erase(0,pos+1);if(line.rfind("[applet_ev]",0)==0){auto i=line.find("code=");if(i!=std::string::npos){int code=atoi(line.c_str()+i+5);self->log("host.applet_event",pid,"{\"code\":"+std::to_string(code)+"}");}}}if(buffer.size()>4096)buffer.erase(0,buffer.size()-4096);}
 bool browser(FridaChild*child){if(cfg.fixture)return true;const char*path=frida_child_get_path(child);if(!path)return false;std::string expected=(fs::u8path(cfg.runtime)/"runtime"/"WeChatAppEx.exe").u8string();if(_stricmp(path,expected.c_str()))return false;gint n=0;auto argv=frida_child_get_argv(child,&n);bool profile=false;for(int i=0;i<n;i++){std::string arg(argv[i]);if(arg.rfind("--type=",0)==0)return false;if(arg.find(cfg.profile)!=std::string::npos)profile=true;}return profile;}
 void drain(bool stopping=false){for(;;){FridaChild*c=nullptr;{std::lock_guard<std::mutex>lock(queueMutex);if(children.empty())break;c=children.front();children.pop();}DWORD pid=frida_child_get_pid(c);if(frida_child_get_parent_pid(c)==hostPID){remember(pid);if(!stopping&&browser(c))attach(pid);}GError*err=nullptr;frida_device_resume_sync(device,pid,nullptr,&err);if(err){g_error_free(err);log("child.resume_error",pid);}g_object_unref(c);}}
 void discover(){HANDLE snap=CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS,0);if(snap==INVALID_HANDLE_VALUE)return;PROCESSENTRY32W entry={sizeof(entry)};std::vector<std::pair<DWORD,DWORD>>all;if(Process32FirstW(snap,&entry))do{all.push_back({entry.th32ProcessID,entry.th32ParentProcessID});}while(Process32NextW(snap,&entry));CloseHandle(snap);for(size_t pass=0;pass<all.size();pass++){bool added=false;for(auto row:all)if(!owned.count(row.first)&&owned.count(row.second)&&alive(row.second)){if(remember(row.first))added=true;}if(!added)break;}}
 static BOOL CALLBACK hideWindow(HWND hwnd,LPARAM data){auto*self=(Observer*)data;DWORD pid=0;GetWindowThreadProcessId(hwnd,&pid);if(self->owned.count(pid)&&self->alive(pid)&&IsWindowVisible(hwnd))ShowWindow(hwnd,SW_HIDE);return TRUE;}
 bool initialize(){if(strcmp(frida_version_string(),"17.2.17")){log("worker.version_error");return false;}events.open(fs::u8path(cfg.out)/"events.jsonl",std::ios::app);manager=frida_device_manager_new();GError*err=nullptr;device=frida_device_manager_get_device_by_type_sync(manager,FRIDA_DEVICE_TYPE_LOCAL,5000,nullptr,&err);if(!device){if(err)g_error_free(err);return false;}job=CreateJobObjectW(nullptr,nullptr);JOBOBJECT_EXTENDED_LIMIT_INFORMATION info={};info.BasicLimitInformation.LimitFlags=JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;if(!job||!SetInformationJobObject(job,JobObjectExtendedLimitInformation,&info,sizeof(info)))return false;std::string event="Local\\WmpfNativeLogin_"+std::to_string(GetCurrentProcessId())+"_"+std::to_string(GetTickCount64());login=CreateEventW(nullptr,TRUE,FALSE,wide(event).c_str());if(!login)return false;source="const launchConfig={appid:"+quote(cfg.appid)+"};\n"+observationSource+"\n"+debugSource;g_signal_connect(device,"child-added",G_CALLBACK(childAdded),this);g_signal_connect(device,"output",G_CALLBACK(onOutput),this);
 std::vector<std::string>env={"WMPF_APPID="+cfg.appid,"WMPF_ROOT="+cfg.profile,"WMPF_RUNTIME_DIR="+cfg.runtime,"WMPF_DATA_DIR="+cfg.out,"WMPF_LOGIN_BUF="+(fs::u8path(cfg.inputs)/"login.txt").u8string(),"WMPF_LAUNCH_CFG="+(fs::u8path(cfg.inputs)/"launch.bin").u8string(),"WMPF_LOGIN_EVENT="+event,"WMPF_USE_CB=1","WMPF_GERR=3000","WMPF_OBS="+std::to_string(cfg.seconds),"WMPF_LOGIN_DELAY=4000","WMPF_OBS_KEEP=1"};
 // The host window (L"wmpf host") is only a hidden top-level HWND for runtime
 // init — it is NOT the mini-program window (that belongs to the browser
 // process). Never auto-show it from hide_window=false; only an explicit
 // diagnostic env var makes it visible. This keeps the real mini-program
 // window visible while the stray host window stays hidden.
 if(getenv("WMPF_HOST_SHOW_WND"))env.push_back("WMPF_SHOW_WND=1");
 std::ifstream kind(fs::u8path(cfg.inputs)/"game_type.txt");std::string game;kind>>game;if(game=="2"||game=="3")env.push_back("WMPF_GAME_TYPE="+game);
 auto opts=frida_spawn_options_new();std::vector<gchar*>argv={(gchar*)cfg.host.c_str()};if(cfg.fixture)argv.push_back((gchar*)"--fixture-host");frida_spawn_options_set_argv(opts,argv.data(),(gint)argv.size());std::vector<std::string>envp;gchar**base=g_get_environ();for(int i=0;base[i];i++)if(_strnicmp(base[i],"WMPF_",5)!=0)envp.push_back(base[i]);g_strfreev(base);for(auto&v:env)envp.push_back(v);std::vector<gchar*>ep;for(auto&v:envp)ep.push_back(v.data());frida_spawn_options_set_envp(opts,ep.data(),(gint)ep.size());frida_spawn_options_set_cwd(opts,fs::u8path(cfg.host).parent_path().u8string().c_str());frida_spawn_options_set_stdio(opts,FRIDA_STDIO_PIPE);hostPID=frida_device_spawn_sync(device,cfg.host.c_str(),opts,nullptr,&err);g_object_unref(opts);if(err||!hostPID){if(err)g_error_free(err);log("host.spawn_error");return false;}HANDLE h=OpenProcess(PROCESS_SET_QUOTA|PROCESS_TERMINATE,FALSE,hostPID);bool assigned=h&&AssignProcessToJobObject(job,h);if(h)CloseHandle(h);remember(hostPID);if(!assigned){log("host.job_error",hostPID);return false;}if(!attach(hostPID))return false;frida_session_enable_child_gating_sync(sessions[hostPID].session,nullptr,&err);if(err){g_error_free(err);return false;}frida_device_resume_sync(device,hostPID,nullptr,&err);if(err){g_error_free(err);return false;}log("worker.started",hostPID);return true;
 }
 int run(){bool ok=initialize();if(ok){ULONGLONG until=GetTickCount64()+cfg.seconds*1000ULL;while(GetTickCount64()<until&&alive(hostPID)&&!loginFailed){while(g_main_context_pending(nullptr))g_main_context_iteration(nullptr,FALSE);drain();discover();if(cfg.hide)EnumWindows(hideWindow,(LPARAM)this);if(fs::exists(fs::u8path(cfg.stop)))break;Sleep(50);}}cleanup();return ok&&!loginFailed?0:1;}
 void cleanup(){
  log("cleanup.begin",hostPID);
  discover();
  // Kill the job before detach: a blocked Frida call must not keep the host alive.
  if(job)TerminateJobObject(job,0);
  for(auto row:owned)if(WaitForSingleObject(row.second,0)==WAIT_TIMEOUT)TerminateProcess(row.second,0);
  ULONGLONG until=GetTickCount64()+3000;
  for(auto row:owned){auto now=GetTickCount64();WaitForSingleObject(row.second,now<until?(DWORD)(until-now):0);}
  std::string remaining="[";bool first=true;
  for(auto row:owned){if(WaitForSingleObject(row.second,0)!=WAIT_OBJECT_0){if(!first)remaining+=",";remaining+=std::to_string(row.first);first=false;}CloseHandle(row.second);}
  remaining+="]";
  JOBOBJECT_BASIC_ACCOUNTING_INFORMATION accounting={};bool queried=false;
  if(job){do{queried=QueryInformationJobObject(job,JobObjectBasicAccountingInformation,&accounting,sizeof(accounting),nullptr)!=FALSE;if(!queried||accounting.ActiveProcesses==0||GetTickCount64()>=until)break;Sleep(20);}while(true);CloseHandle(job);job=nullptr;}
  auto checked=owned.size();owned.clear();
  if(device)g_signal_handlers_disconnect_by_data(device,this);
  // All callbacks are dispatched by the main GLib context on this thread.
  {std::lock_guard<std::mutex>lock(queueMutex);while(!children.empty()){g_object_unref(children.front());children.pop();}}
  for(auto row:sessions){frida_session_detach_sync(row.second.session,nullptr,nullptr);g_signal_handlers_disconnect_by_data(row.second.script,this);g_object_unref(row.second.script);g_object_unref(row.second.session);}sessions.clear();
  if(device){g_object_unref(device);device=nullptr;}
  if(manager){frida_device_manager_close_sync(manager,nullptr,nullptr);g_object_unref(manager);manager=nullptr;}
  if(login){CloseHandle(login);login=nullptr;}
  log(queried&&accounting.ActiveProcesses==0&&first?"cleanup.verified":"cleanup.failed",0,"{\"checked_pids\":"+std::to_string(checked)+",\"remaining_pids\":"+remaining+",\"job_active_processes\":"+(queried?std::to_string(accounting.ActiveProcesses):"null")+"}");
 }
};
static int fixture(bool browser){if(browser){Sleep(15000);return 0;}wchar_t exe[32768];GetModuleFileNameW(nullptr,exe,32768);std::wstring cmd=L"\""+std::wstring(exe)+L"\" --fixture-browser";STARTUPINFOW si={sizeof(si)};PROCESS_INFORMATION pi={};if(!CreateProcessW(nullptr,cmd.data(),nullptr,nullptr,FALSE,CREATE_NO_WINDOW,nullptr,nullptr,&si,&pi))return 4;CloseHandle(pi.hThread);CloseHandle(pi.hProcess);Sleep(15000);return 0;}
int wmain(int argc,wchar_t**argv){if(argc==2&&!wcscmp(argv[1],L"--fixture-browser"))return fixture(true);if(argc==2&&!wcscmp(argv[1],L"--fixture-host"))return fixture(false);if(argc!=3||wcscmp(argv[1],L"--config"))return 2;std::ifstream file(fs::path(argv[2]),std::ios::binary);std::string raw((std::istreambuf_iterator<char>(file)),{});if(raw.empty()||raw.size()>65536)return 2;frida_init();auto p=json_parser_new();if(!json_parser_load_from_data(p,raw.c_str(),raw.size(),nullptr))return 2;JsonNode*n=json_parser_get_root(p);if(!JSON_NODE_HOLDS_OBJECT(n))return 2;auto o=json_node_get_object(n);auto str=[&](const char*k){return std::string(json_object_get_string_member_with_default(o,k,""));};Observer worker;worker.cfg={str("appid"),str("host"),str("runtime"),str("data_root"),str("input_dir"),str("out"),str("stop_file"),str("gate_file"),(int)json_object_get_int_member_with_default(o,"seconds",0),(bool)json_object_get_boolean_member_with_default(o,"hide_window",TRUE),(bool)json_object_get_boolean_member_with_default(o,"fixture",FALSE)};g_object_unref(p);if(worker.cfg.seconds<1||worker.cfg.seconds>1800||worker.cfg.host.empty()||worker.cfg.gate.empty()||worker.cfg.out.empty())return 2;ULONGLONG deadline=GetTickCount64()+10000;while(!fs::exists(fs::u8path(worker.cfg.gate))){if(GetTickCount64()>deadline)return 3;Sleep(20);}return worker.run();}
