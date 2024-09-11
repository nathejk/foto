using Microsoft.Extensions.FileProviders;
using Serilog;

Log.Logger = new LoggerConfiguration().WriteTo.Console().CreateLogger();

var builder = WebApplication.CreateBuilder(args);

builder.Host.UseSerilog();

Directory.CreateDirectory(builder.Configuration["PhotoPath"]!);

// Add services to the container.

builder.Services.AddControllersWithViews();
builder.Services.AddHttpContextAccessor();
builder.Services.AddLogging();

builder.Services.AddCors(o => o.AddDefaultPolicy(p =>
{
    p.AllowAnyOrigin();
    p.AllowAnyMethod();
    p.AllowAnyHeader();
}));

var app = builder.Build();

// Configure the HTTP request pipeline.
if (!app.Environment.IsDevelopment())
{
    // The default HSTS value is 30 days. You may want to change this for production scenarios, see https://aka.ms/aspnetcore-hsts.
    app.UseHsts();
}

app.UseHttpsRedirection();
app.UseStaticFiles();
app.UseRouting();

app.UseCors();

app.MapControllerRoute(name: "default", pattern: "{controller}/{action=Index}/{id?}");

app.MapFallbackToFile("index.html");

app.UseStaticFiles(
    new StaticFileOptions()
    {
        FileProvider = new PhysicalFileProvider(builder.Configuration["PhotoPath"]!),
        RequestPath = new PathString("/photos")
    }
);

app.Run();
