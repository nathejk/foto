using Microsoft.AspNetCore.Mvc;
using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text.Json;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.Metadata.Profiles.Iptc;
using SixLabors.ImageSharp.Processing;

namespace FotoApp.Controllers;

[Route("api/[controller]")]
[ApiController]
public class PhotoController(
    IConfiguration configuration,
    IHttpContextAccessor accessor,
    IHttpClientFactory httpClientFactory,
    ILogger<PhotoController> logger)
    : ControllerBase
{
    /// Guards appends to the failed-notification log against concurrent uploads.
    private static readonly SemaphoreSlim FailedLogLock = new(1, 1);
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

            await NotifyWebhook(teamNumber, type, attention, hostUrl);

            return new OkObjectResult(new { ImageUrl = hostUrl });
        }
        catch (Exception ex)
        {
            logger.LogError(ex, "Failed saving image!");
            return StatusCode(500, $"Internal server error: {ex}");
        }
    }

    /// Advertises a newly stored photo to the configured webhook. Never throws: the photo is
    /// already safely on disk, so a failing receiver must not fail the upload. Failures are
    /// appended as JSON lines to {PhotoPath}/webhook-failed.jsonl so they can be replayed later
    /// without any metadata loss.
    private async Task NotifyWebhook(string teamNumber, string type, bool attention, Uri imageUrl)
    {
        var webhookUrl = configuration.GetValue<string>("WebhookUrl");
        if (string.IsNullOrWhiteSpace(webhookUrl))
        {
            return;
        }

        var payload = new PhotoNotification(
            teamNumber,
            type,
            attention,
            imageUrl.ToString(),
            DateTimeOffset.UtcNow
        );

        try
        {
            var client = httpClientFactory.CreateClient();

            var request = new HttpRequestMessage(HttpMethod.Post, webhookUrl)
            {
                Content = JsonContent.Create(payload),
            };

            var secret = configuration.GetValue<string>("WebhookSecret");
            if (!string.IsNullOrWhiteSpace(secret))
            {
                request.Headers.Add("X-Webhook-Secret", secret);
            }

            var response = await client.SendAsync(request);
            response.EnsureSuccessStatusCode();
            logger.LogInformation("Advertised {ImageUrl} to webhook", imageUrl);
        }
        catch (Exception e)
        {
            logger.LogError(e, "Failed advertising {ImageUrl} to webhook", imageUrl);
            await LogFailedNotification(payload, e);
        }
    }

    private async Task LogFailedNotification(PhotoNotification payload, Exception cause)
    {
        var path = $"{configuration.GetValue<string>("PhotoPath")!.TrimEnd('/')}/webhook-failed.jsonl";

        await FailedLogLock.WaitAsync();
        try
        {
            var line = JsonSerializer.Serialize(
                new
                {
                    payload.TeamNumber,
                    payload.Type,
                    payload.Attention,
                    payload.ImageUrl,
                    payload.CreatedAt,
                    FailedAt = DateTimeOffset.UtcNow,
                    Error = cause.Message,
                }
            );

            await System.IO.File.AppendAllTextAsync(path, line + Environment.NewLine);
        }
        catch (Exception e)
        {
            logger.LogError(e, "Failed writing webhook retry log to {Path}", path);
        }
        finally
        {
            FailedLogLock.Release();
        }
    }

    private record PhotoNotification(
        string TeamNumber,
        string Type,
        bool Attention,
        string ImageUrl,
        DateTimeOffset CreatedAt
    );

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
