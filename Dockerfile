FROM mcr.microsoft.com/dotnet/aspnet:8.0 AS base
RUN apt-get update && apt-get install -y libc6-dev libgdiplus && apt-get clean
WORKDIR /app
ENV ASPNETCORE_HTTP_PORTS=80
EXPOSE 80
EXPOSE 443

FROM mcr.microsoft.com/dotnet/sdk:8.0 AS build
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && apt-get update && apt-get install -y nodejs build-essential && apt-get clean
WORKDIR /src
COPY ["FotoApp.csproj", "."]
RUN dotnet restore "./FotoApp.csproj"
COPY . .
WORKDIR "/src/."
RUN dotnet build "FotoApp.csproj" -c Release -o /app/build

FROM build AS publish
RUN dotnet publish "FotoApp.csproj" -c Release -o /app/publish /p:UseAppHost=false

FROM base AS final
WORKDIR /app
COPY --from=publish /app/publish .
RUN mkdir /photos
ENTRYPOINT ["dotnet", "FotoApp.dll"]