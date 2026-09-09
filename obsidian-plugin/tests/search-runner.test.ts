import { expect, it } from "vitest";
import { CliRunner } from "../src/cli/runner";
const request={projectId:"project-p",expectedGenerationId:"generation-1",queryKind:"file" as const,query:"src/a file.ts",limit:100};
it("passes literal private search text through fixed shell-free argv and rejects wrong generation",async()=>{
 let argv:readonly string[]=[];
 const runner=new CliRunner("/bin/sr",(_file,args,options,callback)=>{argv=args;expect(options.shell).toBe(false);callback(null,JSON.stringify({schema_version:1,project_id:"project-p",generation_id:"generation-1",total:0,items:[],next_cursor:null,previous_cursor:null}),"");});
 expect(typeof runner.getSessionSearch).toBe("function");
 await expect(runner.getSessionSearch(request)).resolves.toMatchObject({total:0});
 expect(argv).toEqual(["inspect","session-search","--project-id","project-p","--expected-generation-id","generation-1","--query-kind","file","--query","src/a file.ts","--limit","100","--json"]);
 const wrong=new CliRunner("/bin/sr",(_file,_args,_options,callback)=>callback(null,JSON.stringify({schema_version:1,project_id:"project-p",generation_id:"generation-old",total:0,items:[],next_cursor:null,previous_cursor:null}),""));
 await expect(wrong.getSessionSearch(request)).rejects.toThrow("无法搜索");
});
