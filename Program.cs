using Microsoft.Extensions.FileProviders;
using Serilog;

Log.Logger = new LoggerConfiguration().WriteTo.Console().CreateLogger();

var builder = WebApplication.CreateBuilder(args);

builder.Host.UseSerilog();

Directory.CreateDirectory(builder.Configuration["PhotoPath"]!);

// Add services to the container.

builder.Services.AddControllersWithViews();
builder.Services.AddHttpContextAccessor();
builder.Services.AddHttpClient();
builder.Services.AddLogging();

builder.Services.AddCors(o => o.AddPolicy("Default", p =>
{
    p.WithOrigins("https://localhost:44480", "https://foto.nathejk.dk");
    p.AllowAnyMethod();
    p.AllowAnyHeader();
}));

var app = builder.Build();

app.UseCors("Default");
app.UseHttpsRedirection();
app.UseRouting();

app.MapControllerRoute(name: "default", pattern: "{controller}/{action=Index}/{id?}");

app.UseStaticFiles();
app.MapFallbackToFile("index.html");
app.UseStaticFiles(
    new StaticFileOptions()
    {
        FileProvider = new PhysicalFileProvider(builder.Configuration["PhotoPath"]!),
        RequestPath = new PathString("/photos")
    }
);

app.Run();
