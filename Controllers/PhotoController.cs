﻿using Microsoft.AspNetCore.Mvc;
using System.Net.Http.Headers;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.Metadata.Profiles.Iptc;
using SixLabors.ImageSharp.Processing;

namespace FotoApp.Controllers;

[Route("api/[controller]")]
[ApiController]
public class PhotoController(
    IConfiguration configuration,
    IHttpContextAccessor accessor,
    ILogger<PhotoController> logger)
    : ControllerBase            
{
    [HttpPost]
    public async Task<IActionResult> Upload(
        [FromQuery] string teamNumber,
        [FromQuery] string type = "start",
        [FromQuery] bool attention = false
    )
    {
        var basedir = configuration.GetValue<string>("PhotoPath");
        if (basedir is null)
        {
            throw new Exception("PhotoPath cannot be null!");
        }

        basedir = $"{basedir.TrimEnd('/')}/{DateTime.UtcNow:yyyy}/{type}";

        try
        {
            Directory.CreateDirectory($"{basedir}/fb");
            var file = Request.Form.Files[0];

            if (file.Length <= 0)
            {
                return BadRequest();
            }

            var extension = Path.GetExtension(
                ContentDispositionHeaderValue.Parse(file.ContentDisposition).FileName?.Trim('"')
            );

            var prefixCounter = 0;
            string fileName;

            var prefix = attention ? "XXX_" : "";
            
            do
            {
                prefixCounter++;
                fileName = $"{prefix}Team-{teamNumber}_{prefixCounter}{extension}";
                logger.LogDebug("Incrementing prefixCounter to {Count}", prefixCounter);
            } while (System.IO.File.Exists($"{basedir}/{fileName}"));

            var fullPath = Path.Combine(basedir, fileName);
            await using (var stream = new FileStream(fullPath, FileMode.Create))
            {
                await file.CopyToAsync(stream);
            }

            try
            {
                using var image = await Image.LoadAsync(fullPath);

                image.Metadata.IptcProfile ??= new IptcProfile();
                image.Metadata.IptcProfile.SetValue(IptcTag.BylineTitle, $"Hold {teamNumber}");
                image.Metadata.IptcProfile.SetValue(IptcTag.Headline, $"Hold {teamNumber}");
                image.Metadata.IptcProfile.SetValue(IptcTag.Name, $"Hold {teamNumber}");
                image.Metadata.IptcProfile.SetValue(IptcTag.Caption, $"Hold {teamNumber}");
                image.Metadata.IptcProfile.SetValue(
                    IptcTag.CopyrightNotice,
                    $"Nathejk {DateTime.UtcNow:yyyy}"
                );

                await image.SaveAsync(fullPath);

                image.Mutate(
                    x =>
                        x.Resize(new ResizeOptions { Mode = ResizeMode.Max, Size = new Size(2000) })
                );

                await image.SaveAsync($"{basedir}/fb/{fileName}");
            }
            catch (Exception e)
            {
                logger.LogError(e, "Failed resizing image");
            }

            var hostUrl = new Uri(
                $"{accessor.HttpContext?.Request.Scheme}://{accessor.HttpContext?.Request.Host}/photos/{DateTime.UtcNow:yyyy}/{type}/{fileName}"
            );

            return new OkObjectResult(new { ImageUrl = hostUrl });
        }
        catch (Exception ex)
        {
            logger.LogError(ex, "Failed saving image!");
            return StatusCode(500, $"Internal server error: {ex}");
        }
    }

    [HttpGet("list/{type}")]
    public IActionResult List([FromRoute] string type)
    {
        var hostUrl = new Uri(
            $"{accessor.HttpContext?.Request.Scheme}://{accessor.HttpContext?.Request.Host}"
        );
        try
        {
            var localFiles = Directory.GetFiles($"{configuration["PhotoPath"]}/{DateTime.UtcNow:yyyy}/{type}/fb");
            var files = localFiles.Select(x => $"{hostUrl}photos/{DateTime.UtcNow:yyyy}/{type}/fb/{Path.GetFileName(x)}");
            return Ok(files);
        }
        catch (DirectoryNotFoundException)
        {
            logger.LogWarning(
                "Client tried to download files from non-existing type \"{Type}\"!",
                type
            );
            return NotFound();
        }
        catch (Exception e)
        {
            logger.LogError(e, "Failed getting photos");
            return BadRequest(e);
        }
    }
}
